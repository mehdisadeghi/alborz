package dav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Past this many taken names, something other than a clash is going on.
const MaxCreateAttempts = 20

// ErrCollectionExists is MKCALENDAR or MKCOL answered 405: the address is taken (RFC 4918 9.3.1).
var ErrCollectionExists = errors.New("a collection of that name is already there")

// MakeCollection sends MKCALENDAR or MKCOL; 201, or 207 from some servers, is success.
func MakeCollection(ctx context.Context, client *http.Client, method, target string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusMultiStatus:
		return nil
	case http.StatusMethodNotAllowed:
		return fmt.Errorf("%s: %w", target, ErrCollectionExists)
	default:
		return fmt.Errorf("failed to create the collection: %s", resp.Status)
	}
}

// CreateCollection makes a collection at the first free address under homeSet, walking past taken ones.
func CreateCollection(ctx context.Context, base *url.URL, homeSet, name, fallback string, make func(ctx context.Context, target string) error) (string, error) {
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
		target := base.ResolveReference(&url.URL{Path: path}).String()
		err := make(ctx, target)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, ErrCollectionExists) || attempt == MaxCreateAttempts {
			return "", err
		}
	}
}

// DeleteCollection removes a collection and all it holds (RFC 4918 9.6).
func DeleteCollection(ctx context.Context, client *http.Client, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("failed to delete the collection: %s", resp.Status)
	}
	return nil
}
