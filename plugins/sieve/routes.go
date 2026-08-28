package alborzsieve

import (
	"fmt"
	"net/http"
	"net/url"

	"git.mehdix.org/alborz"
	"github.com/labstack/echo/v4"
)

type FiltersRenderData struct {
	alborz.BaseRenderData
	Scripts []alborz.SieveScript
}

type FilterRenderData struct {
	alborz.BaseRenderData
	Name    string
	Content string
	Error   string
}

func registerRoutes(p *alborz.GoPlugin) {
	p.GET("/filters", handleListFilters)
	p.GET("/filters/create", handleCreateFilter)
	p.GET("/filters/:name", handleEditFilter)
	p.POST("/filters", handleSaveFilter)
	p.POST("/filters/:name/activate", handleActivateFilter)
	p.POST("/filters/deactivate", handleDeactivateFilter)
	p.POST("/filters/:name/delete", handleDeleteFilter)
}

func filterName(ctx *alborz.Context) (string, error) {
	name, err := url.PathUnescape(ctx.Param("name"))
	if err != nil {
		return "", echo.NewHTTPError(http.StatusBadRequest, err)
	}
	return name, nil
}

func handleListFilters(ctx *alborz.Context) error {
	var scripts []alborz.SieveScript
	err := ctx.Session.DoSieve(func(c alborz.SieveClient) error {
		var err error
		scripts, err = c.ListScripts()
		return err
	})
	if err != nil {
		return err
	}

	return ctx.Render(http.StatusOK, "filters.html", &FiltersRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx),
		Scripts:        scripts,
	})
}

func handleCreateFilter(ctx *alborz.Context) error {
	return ctx.Render(http.StatusOK, "filter-edit.html", &FilterRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx),
	})
}

func handleEditFilter(ctx *alborz.Context) error {
	name, err := filterName(ctx)
	if err != nil {
		return err
	}

	var content string
	err = ctx.Session.DoSieve(func(c alborz.SieveClient) error {
		var err error
		content, err = c.GetScript(name)
		return err
	})
	if err != nil {
		return err
	}

	return ctx.Render(http.StatusOK, "filter-edit.html", &FilterRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx),
		Name:           name,
		Content:        content,
	})
}

func handleSaveFilter(ctx *alborz.Context) error {
	name := ctx.FormValue("name")
	content := ctx.FormValue("content")
	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "missing 'name' form parameter")
	}

	var warnings string
	err := ctx.Session.DoSieve(func(c alborz.SieveClient) error {
		var err error
		warnings, err = c.PutScript(name, content)
		return err
	})
	if err != nil {
		// The server rejects invalid scripts; show the reason next to
		// the script instead of an error page.
		return ctx.Render(http.StatusUnprocessableEntity, "filter-edit.html", &FilterRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx),
			Name:           name,
			Content:        content,
			Error:          err.Error(),
		})
	}

	if warnings != "" {
		ctx.Session.PutNotice(fmt.Sprintf(ctx.T("notice.filterwarn"), warnings))
	} else {
		ctx.Session.PutNotice(ctx.T("notice.filtersaved"))
	}
	return ctx.Redirect(http.StatusFound, "/filters")
}

func handleActivateFilter(ctx *alborz.Context) error {
	name, err := filterName(ctx)
	if err != nil {
		return err
	}

	err = ctx.Session.DoSieve(func(c alborz.SieveClient) error {
		return c.ActivateScript(name)
	})
	if err != nil {
		return err
	}

	ctx.Session.PutNotice(ctx.T("notice.filteron"))
	return ctx.Redirect(http.StatusFound, "/filters")
}

func handleDeactivateFilter(ctx *alborz.Context) error {
	err := ctx.Session.DoSieve(func(c alborz.SieveClient) error {
		return c.ActivateScript("")
	})
	if err != nil {
		return err
	}

	ctx.Session.PutNotice(ctx.T("notice.filteroff"))
	return ctx.Redirect(http.StatusFound, "/filters")
}

func handleDeleteFilter(ctx *alborz.Context) error {
	name, err := filterName(ctx)
	if err != nil {
		return err
	}

	err = ctx.Session.DoSieve(func(c alborz.SieveClient) error {
		return c.DeleteScript(name)
	})
	if err != nil {
		return err
	}

	ctx.Session.PutNotice(ctx.T("notice.filterdeleted"))
	return ctx.Redirect(http.StatusFound, "/filters")
}
