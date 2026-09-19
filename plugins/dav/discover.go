package dav

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/collections"
)

// wellKnownWait bounds asking a mail host for a DAV server at startup:
// a host with none may drop the connection rather than refuse it, and
// every domain is asked in turn.
const wellKnownWait = 3 * time.Second

// davFeature is what a server's DAV header names when it serves the
// service (RFC 4791 5.1, RFC 6352 6.1).
var davFeature = map[string]string{"caldav": "calendar-access", "carddav": "addressbook"}

// OnMailHost finds a DAV server on the mail host itself, as Cyrus and
// Stalwart serve one, for a domain with no SRV record for it: the
// host's /.well-known/<service> (RFC 6764 5), over HTTPS only, since
// the account's password goes there. Without the password a server may
// not name itself, so either sign will do: a DAV header naming the
// service, or the well-known address sending the client on to where it
// is asked to sign in (RFC 6764 5 has it redirect). A web server that
// sends every path to its home page, or asks for a password on every
// one without a redirect, is neither.
func OnMailHost(host, service string) (*url.URL, bool) {
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
	client := &http.Client{Timeout: wellKnownWait, CheckRedirect: func(*http.Request, []*http.Request) error {
		redirected = true
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	resp.Body.Close()
	// Alborz on the mail host answers its own well-known address, and
	// what it points at is alborz's own DAV: that is where collections
	// are kept here, not a second server to offer beside it.
	if landed := resp.Request.URL; landed != nil && strings.HasPrefix(landed.Path, collections.Prefix+"/") {
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
func SanityCheckURL(u *url.URL) error {
	req, err := http.NewRequest(http.MethodOptions, u.String(), nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: alborz.RoundTripTimeout}
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
