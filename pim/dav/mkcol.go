package dav

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/emersion/go-webdav"
)

// Past this many taken names, something other than a clash is going on.
const MaxCreateAttempts = 20

// CreateCollection makes a collection at the first free address under homeSet, walking past taken ones.
func CreateCollection(ctx context.Context, homeSet, name, fallback string, create func(ctx context.Context, path string) error) (string, error) {
	segment := url.PathEscape(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", "-")))
	if segment == "" {
		segment = fallback
	}
	home := CanonicalCollectionPath(homeSet)
	for attempt := 1; ; attempt++ {
		try := segment
		if attempt > 1 {
			try = fmt.Sprintf("%s-%d", segment, attempt)
		}
		path := home + try + "/"
		err := create(ctx, path)
		if err == nil {
			return path, nil
		}
		// A taken address answers 405 (RFC 4918 9.3.1).
		if code, _ := webdav.HTTPErrorCode(err); code != http.StatusMethodNotAllowed || attempt == MaxCreateAttempts {
			return "", err
		}
	}
}
