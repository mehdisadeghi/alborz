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
	"reflect"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/collections"
	"git.mehdix.org/alborz/plugins/davcache"
	"github.com/emersion/go-webdav"
	"github.com/labstack/echo/v4"
)

const requestTimeout = 10 * time.Second

// maxRedirects is how far a DAV server may send a request on; a chain
// longer than this is a loop, not a move.
const maxRedirects = 10

func httpClient(cache *davcache.Cache, here http.Handler, session *alborz.Session, debug echo.Logger) *http.Client {
	return &http.Client{
		// A wedged DAV server fails the request instead of hanging it.
		Timeout: requestTimeout,
		Transport: hereTripper{
			here:    here,
			account: session.Username(),
			remote: cache.Transport(session.Username(), &roundTripper{
				upstream: http.DefaultTransport,
				session:  session,
				debug:    debug,
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

// roundTripper handles authentication and follows redirects while
// preserving the HTTP method. Go's default client changes non-GET/HEAD
// methods to GET on 301/302 redirects, which breaks WebDAV.
type roundTripper struct {
	upstream http.RoundTripper
	session  *alborz.Session

	// Debug logger for upstream DAV traffic; nil keeps it silent.
	// Queries and status lines only, never credentials.
	debug echo.Logger
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

func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := rt.session.SetHTTPBasicAuth(req); err != nil {
		return nil, err
	}

	resp, err := rt.upstream.RoundTrip(req)
	if rt.debug != nil {
		logDAVExchange(rt.debug, req, resp, err)
	}
	if err != nil {
		return nil, err
	}
	// The transport sent the credentials, so it is what knows they were
	// turned down; past here a 401 is one more status in a string.
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, &RefusedError{Host: req.URL.Host}
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		if loc != "" {
			resp.Body.Close()
			return rt.followRedirect(req, loc, maxRedirects)
		}
	}

	return resp, nil
}

func (rt *roundTripper) followRedirect(orig *http.Request, location string, maxRedirects int) (*http.Response, error) {
	if maxRedirects <= 0 {
		return nil, fmt.Errorf("too many redirects")
	}

	locURL, err := orig.URL.Parse(location)
	if err != nil {
		return nil, err
	}

	var body io.ReadCloser
	if orig.GetBody != nil {
		body, err = orig.GetBody()
		if err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequestWithContext(orig.Context(), orig.Method, locURL.String(), body)
	if err != nil {
		return nil, err
	}

	for k, v := range orig.Header {
		if k != "Authorization" {
			req.Header[k] = v
		}
	}

	if err := rt.session.SetHTTPBasicAuth(req); err != nil {
		return nil, err
	}

	resp, err := rt.upstream.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		if loc != "" {
			resp.Body.Close()
			return rt.followRedirect(req, loc, maxRedirects-1)
		}
	}

	return resp, nil
}
