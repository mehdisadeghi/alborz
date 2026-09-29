package mail

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
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

	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "image/") {
		return web.NewError(http.StatusBadRequest, "invalid resource type")
	}

	size, err := strconv.Atoi(resp.Header.Get("Content-Length"))
	if err == nil {
		if size > proxyMaxSize {
			return web.NewError(http.StatusBadRequest, "invalid resource length")
		}
		ctx.Response().Header().Set("Content-Length", strconv.Itoa(size))
	}

	ctx.Response().Header().Set("Cache-Control", proxyCacheControl)
	// The body is the sender's; the browser must not sniff it into another type.
	ctx.Response().Header().Set("X-Content-Type-Options", "nosniff")
	ctx.Response().Header().Set("Content-Disposition", "inline")

	lr := io.LimitedReader{R: resp.Body, N: int64(proxyMaxSize)}
	return ctx.Stream(http.StatusOK, mediaType, &lr)
}
