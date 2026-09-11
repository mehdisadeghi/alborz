package dav

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Advertised is what a DAV server says about itself when asked, and
// nothing alborz worked out for itself. A reader whose calendar behaves
// oddly is looking at somebody else's deployment, and the first thing
// worth knowing is which one and what it claims to do.
type Advertised struct {
	// Host is the server alborz talks to for this account.
	Host string
	// Software is the Server header, which is the server's own claim
	// and often absent.
	Software string
	// Compliance are the classes of the DAV header, in the order the
	// server listed them: "1", "2", "3", "access-control",
	// "calendar-access", "addressbook" and whatever else it supports
	// (RFC 4918 10.1).
	Compliance []string
	// Principal is the current user's principal URL and Home the
	// collection home this account's calendars or address books hang
	// off (RFC 5397, RFC 4791 6.2.1, RFC 6352 7.1.1).
	Principal string
	Home      string
}

// Has reports whether the server listed a compliance class.
func (a Advertised) Has(class string) bool {
	return slices.ContainsFunc(a.Compliance, func(c string) bool {
		return strings.EqualFold(c, class)
	})
}

// Describe asks the server what it is. One OPTIONS, which every DAV
// server answers before anything else happens on a connection anyway;
// the principal and the home come from the caller, which has already
// found them to list the collections.
func Describe(ctx context.Context, client *http.Client, base *url.URL) (Advertised, error) {
	a := Advertised{Host: base.Host}
	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, base.String(), nil)
	if err != nil {
		return a, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return a, err
	}
	defer resp.Body.Close()

	a.Software = resp.Header.Get("Server")
	for _, class := range strings.Split(resp.Header.Get("DAV"), ",") {
		if class = strings.TrimSpace(class); class != "" {
			a.Compliance = append(a.Compliance, class)
		}
	}
	return a, nil
}
