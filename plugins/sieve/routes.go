package alborzsieve

import (
	"fmt"
	"net/http"
	"net/url"

	"git.mehdix.org/alborz"
	"github.com/labstack/echo/v4"
	"strings"
)

type FiltersRenderData struct {
	alborz.BaseRenderData
	Scripts  []alborz.SieveScript
	Accounts []alborz.Account
	// Server is what the account's filters are kept on, so the page says
	// who it is talking to rather than leaving it to be guessed.
	Server string
	// Unreachable is what the filter server said when it would not
	// answer. A provider that is down is not an internal error, and the
	// page says so itself instead of becoming a status page.
	Unreachable string
}

type FilterRenderData struct {
	alborz.BaseRenderData
	Name    string
	Content string
	Error   string

	// Accounts that can hold a script, for the create form's
	// destination; empty when editing, where the script's own account
	// is already settled.
	Accounts []alborz.Account
}

func registerRoutes(p *alborz.GoPlugin) {
	requireAccount := func(h func(*alborz.Context) error) func(*alborz.Context) error {
		return func(ctx *alborz.Context) error {
			if ctx.URLAccount() != "" && ctx.Server.SieveEnabled(ctx.Session.Domain()) {
				return h(ctx)
			}
			if ctx.Request().Method != http.MethodGet {
				return echo.NewHTTPError(http.StatusBadRequest, "filters require an account")
			}
			if !ctx.Unified && ctx.Server.SieveEnabled(ctx.Session.Domain()) {
				target := ctx.Request().URL.Path + "?account=" + alborz.AccountParam(ctx.Session.Username())
				return ctx.Redirect(http.StatusFound, target)
			}
			for _, session := range ctx.Sessions() {
				if ctx.Server.SieveEnabled(session.Domain()) {
					target := ctx.Request().URL.Path + "?account=" + alborz.AccountParam(session.Username())
					return ctx.Redirect(http.StatusFound, target)
				}
			}
			return echo.ErrNotFound
		}
	}
	p.GET("/filters", requireAccount(handleListFilters))
	p.GET("/filters/create", requireAccount(handleCreateFilter))
	p.GET("/filters/:name", requireAccount(handleEditFilter))
	p.POST("/filters", requireAccount(handleSaveFilter))
	p.POST("/filters/:name/activate", requireAccount(handleActivateFilter))
	p.POST("/filters/deactivate", requireAccount(handleDeactivateFilter))
	p.POST("/filters/:name/delete", requireAccount(handleDeleteFilter))
}

func filterName(ctx *alborz.Context) (string, error) {
	name, err := url.PathUnescape(ctx.Param("name"))
	if err != nil {
		return "", echo.NewHTTPError(http.StatusBadRequest, err)
	}
	return name, nil
}

func handleListFilters(ctx *alborz.Context) error {
	data := &FiltersRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("filters.title")),
		Accounts:       sieveAccounts(ctx),
		Server:         ctx.Server.SieveHost(ctx.Session.Domain()),
	}
	err := ctx.DoSieve(func(c alborz.SieveClient) error {
		var err error
		data.Scripts, err = c.ListScripts()
		return err
	})
	if err != nil {
		// The filters live on somebody else's machine, and it being
		// unreachable is news about that machine rather than a fault
		// here. The page says which machine and what it said.
		ctx.Logger().Printf("failed to list sieve scripts on %s: %v", data.Server, err)
		data.Unreachable = err.Error()
		return ctx.Render(http.StatusServiceUnavailable, "filters.html", data)
	}
	return ctx.Render(http.StatusOK, "filters.html", data)
}

// sieveAccounts lists the signed-in accounts whose server holds scripts.
func sieveAccounts(ctx *alborz.Context) []alborz.Account {
	var accounts []alborz.Account
	for _, account := range ctx.Accounts() {
		session := ctx.SessionFor(account.Username)
		if session != nil && ctx.Server.SieveEnabled(session.Domain()) {
			accounts = append(accounts, account)
		}
	}
	return accounts
}

func handleCreateFilter(ctx *alborz.Context) error {
	return ctx.Render(http.StatusOK, "filter-edit.html", &FilterRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx),
		Accounts:       sieveAccounts(ctx),
	})
}

func handleEditFilter(ctx *alborz.Context) error {
	name, err := filterName(ctx)
	if err != nil {
		return err
	}

	var content string
	err = ctx.DoSieve(func(c alborz.SieveClient) error {
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
	if strings.TrimSpace(name) == "" {
		return ctx.Render(http.StatusUnprocessableEntity, "filter-edit.html", &FilterRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx),
			Content:        content,
			Error:          ctx.T("form.nameneeded"),
			Accounts:       sieveAccounts(ctx),
		})
	}

	// A new script names the account it belongs to, the way a new event
	// or contact names its collection; the choice holds for this
	// request only, like following an account-carrying link.
	if account := ctx.FormValue("account"); account != "" && account != ctx.Session.Username() {
		session := ctx.SessionFor(account)
		if session == nil || !ctx.Server.SieveEnabled(session.Domain()) {
			return echo.NewHTTPError(http.StatusBadRequest, "no filters for that account")
		}
		ctx.Session = session
	}

	var warnings string
	err := ctx.DoSieve(func(c alborz.SieveClient) error {
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
			Accounts:       sieveAccounts(ctx),
		})
	}

	if warnings != "" {
		ctx.Session.PutNotice(fmt.Sprintf(ctx.T("notice.filterwarn"), warnings))
	} else {
		ctx.Session.PutNotice(ctx.T("notice.filtersaved"))
	}
	return ctx.Redirect(http.StatusFound, "/filters?account="+alborz.AccountParam(ctx.Session.Username()))
}

func handleActivateFilter(ctx *alborz.Context) error {
	name, err := filterName(ctx)
	if err != nil {
		return err
	}

	err = ctx.DoSieve(func(c alborz.SieveClient) error {
		return c.ActivateScript(name)
	})
	if err != nil {
		return err
	}

	ctx.Session.PutNotice(ctx.T("notice.filteron"))
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/filters"))
}

func handleDeactivateFilter(ctx *alborz.Context) error {
	err := ctx.DoSieve(func(c alborz.SieveClient) error {
		return c.ActivateScript("")
	})
	if err != nil {
		return err
	}

	ctx.Session.PutNotice(ctx.T("notice.filteroff"))
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/filters"))
}

func handleDeleteFilter(ctx *alborz.Context) error {
	name, err := filterName(ctx)
	if err != nil {
		return err
	}

	err = ctx.DoSieve(func(c alborz.SieveClient) error {
		return c.DeleteScript(name)
	})
	if err != nil {
		return err
	}

	ctx.Session.PutNotice(ctx.T("notice.filterdeleted"))
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/filters"))
}
