// Package dav holds what the CalDAV and CardDAV plugins share.
package dav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/collections"
	"github.com/emersion/go-webdav"
	"github.com/labstack/echo/v4"
)

const requestTimeout = 10 * time.Second

// maxRedirects is where a redirect chain is taken for a loop.
const maxRedirects = 10

func httpClient(ctx context.Context, p *Provider, login *alborz.Login) *http.Client {
	return &http.Client{
		// A wedged DAV server fails the request instead of hanging it.
		Timeout: requestTimeout,
		Transport: hereTripper{
			here:    p.here,
			account: login.Username(),
			remote:  p.mirror.Transport(login.Username(), p.router(ctx, login)),
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (p *Provider) router(ctx context.Context, login *alborz.Login) sourceRouter {
	return sourceRouter{
		sources: p.Sources(ctx, login),
		trusted: p.trusted,
		named:   p.named,
		sign: func(req *http.Request, src Source) error {
			if src.Named {
				err := login.SetHTTPBasicAuth(req)
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

// IfNew is an If-None-Match that holds only while nothing is at the address (RFC 7232 3.2).
const IfNew webdav.ConditionalMatch = "*"

// IfUnchanged is an If-Match that holds only while the object is the one read (RFC 7232 3.1).
func IfUnchanged(etag string) webdav.ConditionalMatch {
	if etag == "" {
		return ""
	}
	return webdav.ConditionalMatch(strconv.Quote(etag))
}

// sourceRouter sends a path under a source's marker to that source and marks what comes back (ADR 28).
// It sits below the mirror, which keys by path, so two sources holding one path are two copies.
type sourceRouter struct {
	sources []Source
	// trusted dials the deployment's servers, named the account's own.
	trusted, named http.RoundTripper
	// sign puts the source's login on a request.
	sign func(*http.Request, Source) error

	// debug logs upstream DAV queries and status lines, never credentials; nil keeps it silent.
	debug echo.Logger
}

func (r sourceRouter) source(id string) (Source, error) {
	for _, s := range r.sources {
		if s.ID == id {
			return s, nil
		}
	}
	return Source{}, fmt.Errorf("dav: no source %q for this account", id)
}

func (r sourceRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	defer alborz.AddTiming(req.Context(), "dav", time.Now())
	rest, ok := strings.CutPrefix(req.URL.Path, "/@")
	if !ok {
		src, err := r.source(SourceDomain)
		if err != nil {
			return nil, err
		}
		return r.send(req, src)
	}
	id, path, _ := strings.Cut(rest, "/")
	src, err := r.source(id)
	if err != nil {
		return nil, err
	}
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host, out.Host = src.URL.Scheme, src.URL.Host, ""
	out.URL.Path, out.URL.RawPath = "/"+path, ""
	// A multiget names its objects in the body by marked paths; the source knows them unmarked.
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = hrefPattern.ReplaceAllFunc(body, func(m []byte) []byte {
			parts := hrefPattern.FindSubmatch(m)
			return slices.Concat(parts[1], bytes.TrimPrefix(parts[2], []byte(marker(src.ID))), parts[3])
		})
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		out.ContentLength = int64(len(body))
	}
	resp, err := r.send(out, src)
	if err != nil {
		return nil, err
	}
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

// markHref marks the source's absolute paths and full addresses; other hosts and relative references stay.
func markHref(href string, src Source) string {
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if u.IsAbs() {
		if u.Host != src.URL.Host {
			return href
		}
		u.Scheme, u.Host = "", ""
	}
	if !strings.HasPrefix(u.Path, "/") {
		return href
	}
	u.Path = marker(src.ID) + u.Path
	if u.RawPath != "" {
		u.RawPath = marker(src.ID) + u.RawPath
	}
	return u.String()
}

// hereTripper serves collections kept here in-process, ahead of the mirror, so a share shows the moment it is made.
type hereTripper struct {
	here    http.Handler
	account string
	remote  http.RoundTripper
}

func (t hereTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.here == nil || !strings.HasPrefix(req.URL.Path, collections.Prefix+"/") {
		return t.remote.RoundTrip(req)
	}
	return respond(t.here, req.WithContext(collections.As(req.Context(), t.account))), nil
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
func logDAVExchange(l echo.Logger, req *http.Request, resp *http.Response, err error) {
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

// Answered is err's HTTP status, zero if unanswered; go-webdav's error type is internal, so Code is read by name.
func Answered(err error) int {
	for ; err != nil; err = errors.Unwrap(err) {
		v := reflect.Indirect(reflect.ValueOf(err))
		if v.Kind() != reflect.Struct {
			continue
		}
		if code := v.FieldByName("Code"); code.IsValid() && code.CanInt() {
			return int(code.Int())
		}
	}
	return 0
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
			return nil, err
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
