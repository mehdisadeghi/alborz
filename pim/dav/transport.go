// Package dav holds what the CalDAV and CardDAV plugins share.
package dav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/store"
	"github.com/emersion/go-webdav"
)

const requestTimeout = 10 * time.Second

// unanswered names a server that dropped or refused the connection, or ran out the request's time, as one
// that did not answer; the reader leaving, or a request refused before it left, stays as it was.
func unanswered(req *http.Request, err error) error {
	var dropped net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return alborz.Unanswered("dav", requestTimeout, err)
	case errors.Is(err, context.Canceled) || req.Context().Err() != nil:
		return err
	case errors.As(err, &dropped) && dropped.Timeout():
		return alborz.Unanswered("dav", requestTimeout, err)
	case errors.As(err, &dropped), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return alborz.Unanswered("dav", 0, err)
	}
	return err
}

// maxRedirects is where a redirect chain is taken for a loop.
const maxRedirects = 10

func httpClient(ctx context.Context, p *Provider, login *alborz.Login) *http.Client {
	sources := p.Sources(ctx, login)
	var remote http.RoundTripper = p.router(sources, login)
	// A copy is served only to the name it was kept for; with the name unknown, none is.
	if who, err := principal(ctx, login); err == nil {
		remote = p.mirror.Transport(who, remote)
	}
	return &http.Client{
		// A wedged DAV server fails the request instead of hanging it.
		Timeout: requestTimeout,
		Transport: scaleGuard{
			next: hereTripper{
				sources: sources,
				local:   p.local,
				account: login.Username(),
				remote:  remote,
			},
			colls: func(req *http.Request) ([]Collection, error) { return p.Collections(req.Context(), login) },
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (p *Provider) router(sources []Source, login *alborz.Login) sourceRouter {
	return sourceRouter{
		sources: sources,
		trusted: p.trusted,
		named:   p.named,
		sign: func(req *http.Request, src Source) error {
			if src.Named {
				err := setHTTPBasicAuth(login, req)
				if errors.Is(err, alborz.ErrSealed) {
					return &RefusedError{Host: req.URL.Host}
				}
				return err
			}
			login.SetMailBasicAuth(req)
			return nil
		},
		debug: p.debug,
	}
}

// principal is whom the login's requests to its servers authenticate as.
func principal(ctx context.Context, login *alborz.Login) (Principal, error) {
	services, err := LoadServices(ctx, login.Store())
	if err != nil {
		return Principal{}, err
	}
	return Principal{Account: login.Username(), Name: services.usernameFor(login.Username())}, nil
}

// IfNew is an If-None-Match that holds only while nothing is at the address (RFC 7232 3.2).
const IfNew webdav.ConditionalMatch = "*"

// IfUnchanged is an If-Match that holds only while the object is the one read (RFC 7232 3.1).
// It compares strongly, so a weak ETag, as a compressing proxy makes of Nextcloud's, never matches and is not sent.
func IfUnchanged(etag string) webdav.ConditionalMatch {
	if strings.HasPrefix(etag, "W/") {
		return ""
	}
	return webdav.ConditionalMatch(etag)
}

// sourceRouter sends a marked path to its source and marks what comes back (ADR 28).
// It sits below the mirror, which keys by the marked path, so one path on two servers is two copies.
type sourceRouter struct {
	sources []Source
	// trusted dials the deployment's servers, named the account's own.
	trusted, named http.RoundTripper
	// sign puts the source's login on a request.
	sign func(*http.Request, Source) error

	// debug logs upstream DAV queries and status lines, never credentials; nil keeps it silent.
	debug *log.Logger
}

func (r sourceRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	defer alborz.AddTiming(req.Context(), "dav", time.Now())
	src, out, err := unmarked(r.sources, req)
	if err != nil {
		return nil, err
	}
	resp, err := r.send(out, src)
	if err != nil {
		return nil, err
	}
	return marked(resp, req, src)
}

// unmarked is req as its source knows it, and the source.
func unmarked(sources []Source, req *http.Request) (Source, *http.Request, error) {
	src, path, err := route(sources, req.URL.Path)
	if err != nil {
		return Source{}, nil, err
	}
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host, out.Host = src.URL.Scheme, src.URL.Host, ""
	out.URL.Path, out.URL.RawPath = path, ""
	// A multiget names its objects in the body by marked paths; the source knows them unmarked.
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return Source{}, nil, err
		}
		body = hrefPattern.ReplaceAllFunc(body, func(m []byte) []byte {
			parts := hrefPattern.FindSubmatch(m)
			return slices.Concat(parts[1], bytes.TrimPrefix(parts[2], []byte(marker(src.URL.Host))), parts[3])
		})
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		out.ContentLength = int64(len(body))
	}
	return src, out, nil
}

// marked is the source's answer to req with every href and Location as alborz holds it.
func marked(resp *http.Response, req *http.Request, src Source) (*http.Response, error) {
	// A client takes an object's path from the request its answer names.
	resp.Request = req
	if loc := resp.Header.Get("Location"); loc != "" {
		resp.Header.Set("Location", markHref(loc, src))
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "xml") {
		return resp, nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	body = hrefPattern.ReplaceAllFunc(body, func(m []byte) []byte {
		parts := hrefPattern.FindSubmatch(m)
		return append(append(append([]byte{}, parts[1]...), markHref(string(parts[2]), src)...), parts[3]...)
	})
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Del("Content-Length")
	return resp, nil
}

// hrefPattern is a DAV href element in any prefix a server writes it.
var hrefPattern = regexp.MustCompile(`(<(?:[A-Za-z0-9_.-]+:)?href(?:\s[^>]*)?>)\s*([^<]*?)\s*(</(?:[A-Za-z0-9_.-]+:)?href>)`)

// markHref marks a full address by its own host and an absolute path by the source's; relative references stay.
func markHref(href string, src Source) string {
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	host := src.URL.Host
	if u.IsAbs() {
		host, u.Scheme, u.Host = u.Host, "", ""
	}
	if !strings.HasPrefix(u.Path, "/") {
		return href
	}
	u.Path = marker(host) + u.Path
	if u.RawPath != "" {
		u.RawPath = marker(host) + u.RawPath
	}
	return u.String()
}

// hereTripper answers paths on this alborz in-process, ahead of the mirror, so a share shows the moment it is made.
type hereTripper struct {
	sources []Source
	local   http.Handler
	account string
	remote  http.RoundTripper
}

func (t hereTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	src, _, err := route(t.sources, req.URL.Path)
	if err != nil {
		return nil, err
	}
	if src.ID != SourceHere {
		return t.remote.RoundTrip(req)
	}
	src, out, err := unmarked(t.sources, req)
	if err != nil {
		return nil, err
	}
	return marked(respond(t.local, out.WithContext(store.As(req.Context(), t.account))), req, src)
}

func respond(h http.Handler, req *http.Request) *http.Response {
	wrote := &written{header: make(http.Header), code: http.StatusOK}
	body := req.Body
	h.ServeHTTP(wrote, req)
	if body != nil {
		body.Close()
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", wrote.code, http.StatusText(wrote.code)),
		StatusCode:    wrote.code,
		Proto:         req.Proto,
		ProtoMajor:    req.ProtoMajor,
		ProtoMinor:    req.ProtoMinor,
		Header:        wrote.header,
		Body:          io.NopCloser(&wrote.body),
		ContentLength: int64(wrote.body.Len()),
		Request:       req,
	}
}

func buffered(req *http.Request) (*http.Request, []byte, error) {
	if req.Body == nil {
		return req, nil, nil
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, nil, err
	}
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return out, body, nil
}

type written struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (a *written) Header() http.Header         { return a.header }
func (a *written) WriteHeader(code int)        { a.code = code }
func (a *written) Write(b []byte) (int, error) { return a.body.Write(b) }

// logDAVExchange prints one upstream round trip, re-wrapping the body so the caller still reads it.
func logDAVExchange(l *log.Logger, req *http.Request, resp *http.Response, err error) {
	q := ""
	if req.Method == "REPORT" && req.GetBody != nil {
		if r, e := req.GetBody(); e == nil {
			b, _ := io.ReadAll(r)
			r.Close()
			q = " query=" + string(b)
		}
	}
	if err != nil {
		l.Printf("dav: %s %s error=%v%s", req.Method, req.URL.Path, err, q)
		return
	}
	b, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil {
		l.Printf("dav: %s %s status=%d read error=%v%s", req.Method, req.URL.Path, resp.StatusCode, e, q)
		return
	}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	l.Printf("dav: %s %s status=%d bytes=%d events=%d todos=%d cards=%d%s",
		req.Method, req.URL.Path, resp.StatusCode, len(b),
		bytes.Count(b, []byte("BEGIN:VEVENT")), bytes.Count(b, []byte("BEGIN:VTODO")),
		bytes.Count(b, []byte("BEGIN:VCARD")), q)
}

// RefusedError is a DAV server refusing the account's credentials: an answer the reader can act on.
type RefusedError struct {
	Host string
}

func (e *RefusedError) Error() string { return e.Host + " refused the account's password" }

// ServerRefused says whether err is a DAV server's answer rather than a failure to get one:
// a status it sent, its refusal of the password, or a scale it does not list.
func ServerRefused(err error) bool {
	_, answered := webdav.HTTPErrorCode(err)
	var password *RefusedError
	return answered || errors.As(err, &password) || errors.Is(err, ErrScaleUnlisted)
}

// send follows redirects keeping the method, since Go's client turns a redirected PROPFIND into a GET.
// The login goes to the source's host only, TLS is never left, and a 401 from any hop is a refusal.
func (r sourceRouter) send(req *http.Request, src Source) (*http.Response, error) {
	upstream := r.trusted
	if src.ID == SourceOwn {
		upstream = r.named
	}
	req = req.Clone(req.Context())
	for range maxRedirects {
		req.Header.Del("Authorization")
		if req.URL.Host == src.URL.Host {
			if err := r.sign(req, src); err != nil {
				return nil, err
			}
		}
		resp, err := upstream.RoundTrip(req)
		if r.debug != nil {
			logDAVExchange(r.debug, req, resp, err)
		}
		if err != nil {
			return nil, unanswered(req, err)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			return nil, &RefusedError{Host: req.URL.Host}
		}
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			return resp, nil
		}
		resp.Body.Close()
		to, err := req.URL.Parse(loc)
		if err != nil {
			return nil, err
		}
		if to.Scheme != "https" && to.Scheme != src.URL.Scheme {
			return nil, fmt.Errorf("dav: %s redirects to %s, off TLS", req.URL.Host, to)
		}
		next := req.Clone(req.Context())
		next.URL, next.Host = to, ""
		if req.GetBody != nil {
			if next.Body, err = req.GetBody(); err != nil {
				return nil, err
			}
		}
		req = next
	}
	return nil, fmt.Errorf("dav: %s redirects more than %d times", src.URL.Host, maxRedirects)
}
