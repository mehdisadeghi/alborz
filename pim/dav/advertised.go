package dav

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Advertised is what a DAV server claims about itself, nothing alborz inferred.
type Advertised struct {
	// Software is the Server header, often absent.
	Software string
	// Compliance are the DAV header's classes in server order (RFC 4918 10.1).
	Compliance []string
	// Principal is the current user's principal URL, Home the collection home (RFC 5397, RFC 4791 6.2.1, RFC 6352 7.1.1).
	Principal string
	Home      string
}

// Has reports whether the server listed a compliance class.
func (a Advertised) Has(class string) bool {
	return slices.ContainsFunc(a.Compliance, func(c string) bool {
		return strings.EqualFold(c, class)
	})
}

// Describe asks the server what it is with one OPTIONS request.
func Describe(ctx context.Context, client *http.Client, base *url.URL) (Advertised, error) {
	var a Advertised
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
