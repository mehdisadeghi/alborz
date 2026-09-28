package dav

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/store"
)

// A host without DAV may drop the connection rather than refuse it, and every domain is asked in turn.
const wellKnownWait = 3 * time.Second

// davFeature is the DAV header class naming the service (RFC 4791 5.1, RFC 6352 6.1).
var davFeature = map[string]string{"caldav": "calendar-access", "carddav": "addressbook"}

// OnMailHost finds DAV at the mail host's /.well-known/<service> (RFC 6764 5), HTTPS only since the password goes there.
func OnMailHost(rt http.RoundTripper, host, service string) (*url.URL, bool) {
	hostname, _, err := net.SplitHostPort(host)
	if err != nil {
		hostname = host
	}
	u := &url.URL{Scheme: "https", Host: hostname, Path: "/.well-known/" + service}
	req, err := http.NewRequest(http.MethodOptions, u.String(), nil)
	if err != nil {
		return nil, false
	}
	redirected := false
	client := &http.Client{Transport: rt, Timeout: wellKnownWait, CheckRedirect: func(*http.Request, []*http.Request) error {
		redirected = true
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	resp.Body.Close()
	// A well-known address leading to alborz's own DAV is not a second server.
	if landed := resp.Request.URL; landed != nil && strings.HasPrefix(landed.Path, store.Prefix+"/") {
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

// SanityCheckURL asks the endpoint whether it is there at all.
func SanityCheckURL(rt http.RoundTripper, u *url.URL) error {
	req, err := http.NewRequest(http.MethodOptions, u.String(), nil)
	if err != nil {
		return err
	}

	client := &http.Client{Transport: rt, Timeout: alborz.RoundTripTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("HTTP request failed: %v %v", resp.StatusCode, resp.Status)
	}
	return nil
}
