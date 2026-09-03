// Package dav holds what the CalDAV and CardDAV plugins share: the
// transport that authenticates and follows redirects, the PROPFIND
// that lists collections, and the small requests that manage them.
package dav

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/davcache"
	"github.com/labstack/echo/v4"
)

const requestTimeout = 10 * time.Second

func httpClient(cache *davcache.Cache, session *alborz.Session, debug echo.Logger) *http.Client {
	return &http.Client{
		// A wedged DAV server fails the request instead of hanging it.
		Timeout: requestTimeout,
		Transport: cache.Transport(session.Username(), &roundTripper{
			upstream: http.DefaultTransport,
			session:  session,
			debug:    debug,
		}),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

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

func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.session.SetHTTPBasicAuth(req)

	resp, err := rt.upstream.RoundTrip(req)
	if rt.debug != nil {
		logDAVExchange(rt.debug, req, resp, err)
	}
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		if loc != "" {
			resp.Body.Close()
			return rt.followRedirect(req, loc, 10)
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

	rt.session.SetHTTPBasicAuth(req)

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
