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
	proxyEnabled = true
	proxyMaxSize = 5 * 1024 * 1024 // 5 MiB
	// A sender's server is not ours and owes us nothing: a slow one
	// must not hold a request of ours open.
	proxyTimeout = 10 * time.Second
	// The browser may keep what it has already been shown, so scrolling
	// back through a message does not fetch it again.
	proxyCacheControl = "private, max-age=86400"
	proxyClient       = &http.Client{Timeout: proxyTimeout}
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
		if !proxyEnabled {
			return echo.NewHTTPError(http.StatusForbidden, "proxy disabled")
		}

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
		resp, err := proxyClient.Do(req)
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

		lr := io.LimitedReader{resp.Body, int64(proxyMaxSize)}
		return ctx.Stream(http.StatusOK, mediaType, &lr)
	})

	alborz.RegisterPluginLoader(p.Loader())
}
