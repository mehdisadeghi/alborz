// Package dav holds what the CalDAV and CardDAV plugins share: the
// transport that authenticates and follows redirects, the PROPFIND
// that lists collections, and the small requests that manage them.
package dav

import (
	"bytes"
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

// maxRedirects is how far a DAV server may send a request on; a chain
// longer than this is a loop, not a move.
const maxRedirects = 10

func httpClient(p *Provider, session *alborz.Session) *http.Client {
	return &http.Client{
		// A wedged DAV server fails the request instead of hanging it.
		Timeout: requestTimeout,
		Transport: hereTripper{
			here:    p.here,
			account: session.Username(),
			remote: p.cache.Transport(session.Username(), sourceRouter{
				sources: p.Sources(session),
				trusted: p.trusted,
				named:   p.named,
				sign: func(req *http.Request, src Source) error {
					if src.Named {
						return session.SetHTTPBasicAuth(req)
					}
					session.SetMailBasicAuth(req)
					return nil
				},
				debug: p.debug,
			}),
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// IfNew is a PUT's If-None-Match that holds only while nothing is at
// the address (RFC 7232 3.2).
const IfNew webdav.ConditionalMatch = "*"

// IfUnchanged is a PUT's If-Match that holds only while the object is
// the one that was read (RFC 7232 3.1). A server that gave no ETag gets
// no condition.
func IfUnchanged(etag string) webdav.ConditionalMatch {
	if etag == "" {
		return ""
	}
	return webdav.ConditionalMatch(strconv.Quote(etag))
}

// sourceRouter sends a path under a source's marker to that source
// (ADR 28): the host is the source's, the marker comes off on the way
// out and goes back on every href and Location on the way in, so what
// is above it - the cache, the pages - sees paths that name the source.
// It sits below the cache, which keys by path, so two sources holding
// the same path are two entries.
//
// The source decides everything about how a request leaves: the login
// it carries, the transport that dials it, and where a redirect may
// take it.
type sourceRouter struct {
	sources []Source
	// trusted dials the deployment's servers, named the account's own.
	trusted, named http.RoundTripper
	// sign puts the source's login on a request.
	sign func(*http.Request, Source) error

	// Debug logger for upstream DAV traffic; nil keeps it silent.
	// Queries and status lines only, never credentials.
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
	// A multiget names its objects in the body, by the paths the pages
	// know them by; the source knows them without the marker.
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

// markHref puts the source's marker on a path the source answered with:
// an absolute path, or its own full address. Anything else - another
// host, a relative reference - is left as the server wrote it.
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

// hereTripper answers a request for a collection kept here without it
// leaving the process, as the session's account, and ahead of the
// cache: the store is as near as the cache is, and a share has to show
// the moment it is made.
type hereTripper struct {
	here    http.Handler
	account string
	remote  http.RoundTripper
}

func (t hereTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.here == nil || !strings.HasPrefix(req.URL.Path, collections.Prefix+"/") {
		return t.remote.RoundTrip(req)
	}
	wrote := &written{header: make(http.Header), code: http.StatusOK}
	t.here.ServeHTTP(wrote, req.WithContext(collections.As(req.Context(), t.account)))
	if req.Body != nil {
		req.Body.Close()
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
	}, nil
}

// written is what the handler wrote, kept to be read as a response.
type written struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (a *written) Header() http.Header         { return a.header }
func (a *written) WriteHeader(code int)        { a.code = code }
func (a *written) Write(b []byte) (int, error) { return a.body.Write(b) }

// logDAVExchange prints one upstream DAV round trip: the query for
// REPORTs, the status, and what kind of payload came back. Response
// bodies are re-wrapped so the caller still reads them.
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

// RefusedError is a DAV server saying no to the account's credentials.
// It is an answer, and a different one from no answer: a mail account
// often has no DAV account behind it, or one with a password of its own,
// and the reader can do something about that and nothing about a server
// that is down.
type RefusedError struct {
	Host string
}

func (e *RefusedError) Error() string { return e.Host + " refused the account's password" }

// Answered is the HTTP status a DAV server answered with, where err
// carries one, and zero where the server did not answer at all.
// go-webdav keeps its error type internal and its Code field exported,
// so the field is read by name.
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

// send signs a request for its source, sends it, and follows where the
// server sends it on, keeping the method: Go's own client turns a
// redirected PROPFIND into a GET, which breaks WebDAV. Each hop is
// judged on its own. The login goes to the source's host and no other;
// a source reached over TLS is not followed off it; and a 401 from any
// hop is the server turning the login down, which past here would be
// one more status in a string.
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
