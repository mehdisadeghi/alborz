package dav

import (
	"net/url"
	"path"
	"strings"
)

// CanonicalCollectionPath normalizes a collection href; the trailing slash avoids redirects and prefix collisions.
func CanonicalCollectionPath(href string) string {
	href = strings.TrimSpace(href)
	if u, err := url.Parse(href); err == nil {
		href = u.Path
	}
	if !strings.HasPrefix(href, "/") {
		href = "/" + href
	}
	href = path.Clean(href)
	if href != "/" {
		href += "/"
	}
	return href
}
