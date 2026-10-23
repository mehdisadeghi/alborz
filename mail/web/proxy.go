package web

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz/web"
)

var (
	proxyMaxSize = 5 * 1024 * 1024
	// A sender's slow server must not hold our request open.
	proxyTimeout = 10 * time.Second
	// Scrolling back through a message does not fetch its images again.
	proxyCacheControl = "private, max-age=86400"
)

// handleProxy serves a message's remote images.
func handleProxy(ctx *web.Context) error {
	u, err := url.Parse(ctx.QueryParam("src"))
	if err != nil {
		return web.NewError(http.StatusBadRequest, "invalid URL")
	}

	if u.Scheme != "https" {
		return web.NewError(http.StatusBadRequest, "invalid scheme")
	}

	req, err := http.NewRequestWithContext(ctx.Request().Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return web.NewError(http.StatusBadRequest, "invalid URL")
	}
	resp, err := ctx.Server.Outgoing.Remote(proxyTimeout).Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch remote resource within %v: %v", proxyTimeout, err)
	}
	defer resp.Body.Close()

	body, mediaType, err := proxyImageBody(resp)
	if err != nil {
		return err
	}

	ctx.Response().Header().Set("Cache-Control", proxyCacheControl)
	// The body is the sender's; the browser must not sniff it into another type.
	ctx.Response().Header().Set("X-Content-Type-Options", "nosniff")
	ctx.Response().Header().Set("Content-Disposition", "inline")

	return ctx.Blob(http.StatusOK, mediaType, body)
}

// Buffer the bounded response before committing: truncated images must not be cached as successes.
func proxyImageBody(resp *http.Response) ([]byte, string, error) {
	if resp.StatusCode != http.StatusOK {
		return nil, "", web.NewError(http.StatusBadGateway, "image server refused the request")
	}
	if resp.ContentLength > int64(proxyMaxSize) {
		return nil, "", web.NewError(http.StatusBadRequest, "invalid resource length")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(proxyMaxSize)+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > proxyMaxSize {
		return nil, "", web.NewError(http.StatusBadRequest, "invalid resource length")
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType = http.DetectContentType(body)
	}
	if !strings.HasPrefix(mediaType, "image/") {
		return nil, "", web.NewError(http.StatusBadRequest, "invalid resource type")
	}
	return body, mediaType, nil
}
