package alpscarddav

import (
	"fmt"
	"io"
	"net/http"
	"net/url"

	"git.sr.ht/~migadu/alps"
	"github.com/emersion/go-webdav/carddav"
)

var errNoAddressBook = fmt.Errorf("carddav: no address book found")

// webdavRoundTripper handles authentication and follows redirects while
// preserving the HTTP method. Go's default client changes non-GET/HEAD
// methods to GET on 301/302 redirects, which breaks WebDAV.
type webdavRoundTripper struct {
	upstream http.RoundTripper
	session  *alps.Session
}

func (rt *webdavRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.session.SetHTTPBasicAuth(req)

	resp, err := rt.upstream.RoundTrip(req)
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

func (rt *webdavRoundTripper) followRedirect(orig *http.Request, location string, maxRedirects int) (*http.Response, error) {
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

func newClient(u *url.URL, session *alps.Session) (*carddav.Client, error) {
	rt := &webdavRoundTripper{
		upstream: http.DefaultTransport,
		session:  session,
	}
	httpClient := &http.Client{
		Transport: rt,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return carddav.NewClient(httpClient, u.String())
}

type AddressObject struct {
	*carddav.AddressObject
}

func newAddressObjectList(aos []carddav.AddressObject) []AddressObject {
	l := make([]AddressObject, len(aos))
	for i := range aos {
		l[i] = AddressObject{&aos[i]}
	}
	return l
}

func (ao AddressObject) URL() string {
	return "/contacts/" + url.PathEscape(ao.Path)
}
