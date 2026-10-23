package dav

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/emersion/go-webdav"
)

var (
	ErrUnversionedObject      = errors.New("the server supplied no strong ETag for the source")
	ErrUnversionedDestination = errors.New("the destination supplied no strong ETag; the source was kept")
	ErrMoveUncertain          = errors.New("source removal could not be confirmed; the destination was kept")
)

// DeleteRefused distinguishes an explicit refusal from an uncertain deletion.
// Even after a refusal, confirm that the source still exists before rollback.
func DeleteRefused(err error) bool {
	code, _ := webdav.HTTPErrorCode(err)
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusMethodNotAllowed,
		http.StatusConflict, http.StatusPreconditionFailed, http.StatusLocked:
		return true
	}
	return false
}

// SourceExists checks the server directly: HEAD is never served by the mirror.
func SourceExists(ctx context.Context, client *http.Client, path string) (bool, error) {
	u, err := url.Parse(path)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, Base().ResolveReference(u).String(), nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode/100 != 2 {
		return false, webdav.NewHTTPError(resp.StatusCode, errors.New(resp.Status))
	}
	return true, nil
}

// RemoveObject deletes only the version read. Without a strong ETag a transfer
// cannot safely delete its source or roll back a destination changed elsewhere.
func RemoveObject(ctx context.Context, client *http.Client, path, etag string) error {
	if etag == "" || strings.HasPrefix(etag, "W/") {
		return webdav.NewHTTPError(http.StatusPreconditionFailed, ErrUnversionedObject)
	}
	u, err := url.Parse(path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, Base().ResolveReference(u).String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("If-Match", etag)
	req.Header.Set("Schedule-Reply", "F")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return webdav.NewHTTPError(resp.StatusCode, errors.New(resp.Status))
	}
	return nil
}
