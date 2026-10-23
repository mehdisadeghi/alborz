package dav

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/emersion/go-webdav"
)

// MoveObject asks one DAV source to move a resource without replacing another.
// Only an explicit lack of MOVE support permits a copy-and-delete fallback.
func MoveObject(ctx context.Context, client *http.Client, from, to, etag string) (handled bool, err error) {
	if etag == "" || strings.HasPrefix(etag, "W/") {
		return true, webdav.NewHTTPError(http.StatusPreconditionFailed, ErrUnversionedObject)
	}
	source, err := url.Parse(from)
	if err != nil {
		return true, err
	}
	destination, err := url.Parse(to)
	if err != nil {
		return true, err
	}
	req, err := http.NewRequestWithContext(ctx, "MOVE", Base().ResolveReference(source).String(), nil)
	if err != nil {
		return true, err
	}
	req.Header.Set("Destination", Base().ResolveReference(destination).String())
	req.Header.Set("Overwrite", "F")
	req.Header.Set("If-Match", etag)
	resp, err := client.Do(req)
	if err != nil {
		return true, errors.Join(ErrMoveUncertain, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusNoContent:
		return true, nil
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return false, nil
	default:
		err := webdav.NewHTTPError(resp.StatusCode, errors.New(resp.Status))
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusMultiStatus || resp.StatusCode == http.StatusNotFound {
			return true, errors.Join(ErrMoveUncertain, err)
		}
		return true, err
	}
}
