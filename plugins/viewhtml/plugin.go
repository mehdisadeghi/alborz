package alborzviewhtml

import (
	"embed"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"github.com/labstack/echo/v4"
)

//go:embed all:public
var public embed.FS

var (
	proxyMaxSize = 5 * 1024 * 1024
	// A sender's slow server must not hold our request open.
	proxyTimeout = 10 * time.Second
	// Scrolling back through a message does not fetch its images again.
	proxyCacheControl = "private, max-age=86400"
)

func init() {
	p := alborz.GoPlugin{Name: "viewhtml", Files: public}

	p.Inject("message.html", func(ctx *alborz.Context, _data alborz.RenderData) error {
		data := _data.(*alborzbase.MessageRenderData)
		data.Extra["RemoteResourcesAllowed"] = ctx.QueryParam("allow-remote-resources") == "1"
		hasRemoteResources := false
		if v := ctx.Get("viewhtml.hasRemoteResources"); v != nil {
			hasRemoteResources = v.(bool)
		}
		data.Extra["HasRemoteResources"] = hasRemoteResources
		return nil
	})

	p.GET("/proxy", func(ctx *alborz.Context) error {
		u, err := url.Parse(ctx.QueryParam("src"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid URL")
		}

		if u.Scheme != "https" {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid scheme")
		}

		req, err := http.NewRequestWithContext(ctx.Request().Context(), http.MethodGet, u.String(), nil)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid URL")
		}
		resp, err := ctx.Server.Outgoing.Remote(proxyTimeout).Do(req)
		if err != nil {
			return fmt.Errorf("failed to fetch remote resource within %v: %v", proxyTimeout, err)
		}
		defer resp.Body.Close()

		mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if err != nil || !strings.HasPrefix(mediaType, "image/") {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid resource type")
		}

		size, err := strconv.Atoi(resp.Header.Get("Content-Length"))
		if err == nil {
			if size > proxyMaxSize {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid resource length")
			}
			ctx.Response().Header().Set("Content-Length", strconv.Itoa(size))
		}

		ctx.Response().Header().Set("Cache-Control", proxyCacheControl)
		// The body is the sender's; the browser must not sniff it into another type.
		ctx.Response().Header().Set("X-Content-Type-Options", "nosniff")
		ctx.Response().Header().Set("Content-Disposition", "inline")

		lr := io.LimitedReader{R: resp.Body, N: int64(proxyMaxSize)}
		return ctx.Stream(http.StatusOK, mediaType, &lr)
	})

	alborz.RegisterPluginLoader(p.Loader())
}
