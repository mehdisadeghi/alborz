package dav

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz"
)

// A host without DAV may drop the connection rather than refuse it, and every domain is asked in turn.
const wellKnownWait = 3 * time.Second

// davFeature is the DAV header class naming the service (RFC 4791 5.1, RFC 6352 6.1).
var davFeature = map[string]string{"caldav": "calendar-access", "carddav": "addressbook"}

func options(rt http.RoundTripper, u *url.URL, timeout time.Duration, redirect func(*http.Request, []*http.Request) error) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodOptions, u.String(), nil)
	if err != nil {
		return nil, err
	}
	return (&http.Client{Transport: rt, Timeout: timeout, CheckRedirect: redirect}).Do(req)
}

// OnMailHost finds DAV at the mail host's /.well-known/<service> (RFC 6764 5), HTTPS only since the password goes there;
// an answer naming instance is this alborz, not a second server.
func OnMailHost(rt http.RoundTripper, host, service, instance string) (*url.URL, bool) {
	hostname, _, err := net.SplitHostPort(host)
	if err != nil {
		hostname = host
	}
	u := &url.URL{Scheme: "https", Host: hostname, Path: "/.well-known/" + service}
	redirected := false
	resp, err := options(rt, u, wellKnownWait, func(*http.Request, []*http.Request) error {
		redirected = true
		return nil
	})
	if err != nil {
		return nil, false
	}
	resp.Body.Close()
	if resp.Header.Get(alborz.InstanceHeader) == instance {
		return nil, false
	}
	for _, v := range resp.Header.Values("DAV") {
		for _, feature := range strings.Split(v, ",") {
			if strings.TrimSpace(feature) == davFeature[service] {
				return u, true
			}
		}
	}
	if redirected && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusMultiStatus) {
		return u, true
	}
	return nil, false
}

// answersAs says whether the server at u names instance in its answer (alborz.InstanceHeader).
func answersAs(rt http.RoundTripper, u *url.URL, instance string) bool {
	resp, err := options(rt, u, alborz.RoundTripTimeout, nil)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.Header.Get(alborz.InstanceHeader) == instance
}

// SanityCheckURL asks the endpoint whether it is there at all.
func SanityCheckURL(rt http.RoundTripper, u *url.URL) error {
	resp, err := options(rt, u, alborz.RoundTripTimeout, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("HTTP request failed: %v %v", resp.StatusCode, resp.Status)
	}
	return nil
}
