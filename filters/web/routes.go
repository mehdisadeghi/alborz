package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/filters"
	"git.mehdix.org/alborz/web"
)

type FiltersRenderData struct {
	web.PageData[map[string][]web.RailRow]
	// Groups holds every account with scripts on a bare URL, else the one the URL names.
	Groups []AccountFilters
	// Rows are the scripts of every group, one page of them.
	Rows     []ScriptRow
	Pager    web.Pager
	Accounts []web.Account
	// Explained is shown once for the page rather than in every server card.
	Explained []web.Explained
}

// ScriptRow is a script as the list shows it, with the account holding it.
type ScriptRow struct {
	alborz.SieveScript
	Account string
}

// AccountFilters is one account's scripts and the server they live on.
type AccountFilters struct {
	Account    string
	Scripts    []alborz.SieveScript
	Server     string
	Software   string
	Extensions []Extension
	Explained  []web.Explained
	// Unreachable is the filter server's refusal, shown on the page rather than as an error.
	Unreachable string
}

type FilterRenderData struct {
	web.PageData[map[string][]web.RailRow]
	Name      string
	Content   string
	Loaded    string
	Suggested string

	Accounts []web.Account
	Account  string
}

func newFilterRenderData(ctx *web.Context, title, refused string) *FilterRenderData {
	page := web.NewPageData(ctx, title, rail(ctx))
	if refused != "" {
		page.WithRefused(refused)
	}
	return &FilterRenderData{PageData: *page}
}

func registerRoutes(g *web.Group) {
	requireAccount := func(h func(*web.Context) error) func(*web.Context) error {
		return func(ctx *web.Context) error {
			if ctx.URLAccount() != "" && ctx.Server.SieveEnabled(ctx.Login.Domain()) {
				return h(ctx)
			}
			if ctx.Request().Method != http.MethodGet {
				return web.NewError(http.StatusBadRequest, "filters require an account")
			}
			if !ctx.Unified && ctx.Server.SieveEnabled(ctx.Login.Domain()) {
				return ctx.FoundRedirect(web.AccountURL(ctx.Request().URL.Path, ctx.Login.Username()))
			}
			for _, login := range ctx.Logins() {
				if ctx.Server.SieveEnabled(login.Domain()) {
					return ctx.FoundRedirect(web.AccountURL(ctx.Request().URL.Path, login.Username()))
				}
			}
			return web.NewError(http.StatusNotFound)
		}
	}
	g.GET("list", "", handleListFilters)
	g.GET("create", "/create", handleCreateFilter)
	g.GET("import", "/import", handleImportFilter)
	g.Upload("import", "/import", maxScriptSize+web.FormFieldsSize, handleImportFilter, handleImportFilterTooLarge)
	g.GET("forwarding", "/forwarding", requireAccount(handleForwarding))
	g.GET("forwarding.create", "/forwarding/create", requireAccount(handleForwardingCreate))
	g.POST("forwarding", "/forwarding", requireAccount(handleForwardingAdd))
	g.POST("forwarding.delete", "/forwarding/delete", requireAccount(handleForwardingDelete))
	g.POST("forwarding.keep", "/forwarding/keep", requireAccount(handleForwardingKeep))
	g.POST("forwarding.toggle", "/forwarding/toggle", requireAccount(handleForwardingToggle))
	g.GET("rules", "/rules", requireAccount(handleRules))
	g.GET("rules.create", "/rules/create", requireAccount(handleRuleForm))
	g.POST("rules.block", "/rules/block", requireAccount(handleBlock))
	g.GET("rule", "/rules/{index}", requireAccount(handleRuleForm))
	g.POST("rules", "/rules", requireAccount(handleRuleSave))
	g.POST("rules.delete", "/rules/delete", requireAccount(handleRuleDelete))
	g.POST("rules.move", "/rules/move", requireAccount(handleRuleMove))
	g.POST("rules.toggle", "/rules/toggle", requireAccount(handleRuleToggle))
	g.GET("autoreply", "/autoreply", requireAccount(handleReplies))
	g.GET("autoreply.create", "/autoreply/create", requireAccount(handleReplyForm))
	g.GET("autoreply.reply", "/autoreply/{index}", requireAccount(handleReplyForm))
	g.POST("autoreply", "/autoreply", requireAccount(handleReplySave))
	g.POST("autoreply.delete", "/autoreply/delete", requireAccount(handleReplyDelete))
	g.POST("autoreply.activate", "/autoreply/activate", requireAccount(handleReplyActivate))
	g.GET("filter", "/{name}", requireAccount(handleEditFilter))
	g.POST("list", "", requireAccount(handleSaveFilter))
	g.POST("filter.activate", "/{name}/activate", requireAccount(handleActivateFilter))
	g.POST("deactivate", "/deactivate", requireAccount(handleDeactivateFilter))
	g.POST("filter.delete", "/{name}/delete", requireAccount(handleDeleteFilter))
}

func handleListFilters(ctx *web.Context) error {
	data := &FiltersRenderData{
		PageData: *web.NewPageData(ctx, ctx.T("filters.title"), rail(ctx)),
		Accounts: sieveAccounts(ctx),
	}
	unreachable := 0
	for _, account := range data.Accounts {
		if ctx.URLAccount() != "" && account.Username != ctx.URLAccount() {
			continue
		}
		login := ctx.LoginFor(account.Username)
		group := AccountFilters{Account: account.Username, Server: ctx.Server.SieveHost(login.Domain())}
		err := login.DoSieve(func(c alborz.SieveClient) error {
			var err error
			group.Scripts, err = c.ListScripts()
			group.Software = c.Implementation()
			group.Extensions = describe(c.Extensions())
			for _, e := range group.Extensions {
				if e.Hint != "" {
					group.Explained = append(group.Explained,
						web.Explained{Term: e.Name, Hint: ctx.T(e.Hint)})
				}
			}
			return err
		})
		if err != nil {
			// An unreachable filter server is news about that server, not a fault here.
			ctx.Logger().Printf("failed to list sieve scripts on %s: %v", group.Server, err)
			group.Unreachable = err.Error()
			unreachable++
		}
		data.Groups = append(data.Groups, group)
	}
	var rows []ScriptRow
	for _, group := range data.Groups {
		for _, script := range group.Scripts {
			rows = append(rows, ScriptRow{SieveScript: script, Account: group.Account})
		}
	}
	var err error
	if data.Rows, data.Pager, err = web.Paginate(ctx, rows); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, group := range data.Groups {
		for _, e := range group.Explained {
			if !seen[e.Term] {
				seen[e.Term] = true
				data.Explained = append(data.Explained, e)
			}
		}
	}
	status := http.StatusOK
	if len(data.Groups) > 0 && unreachable == len(data.Groups) {
		status = http.StatusServiceUnavailable
	}
	return ctx.Render(status, "filters/list", data)
}

func sieveAccounts(ctx *web.Context) []web.Account {
	var accounts []web.Account
	for _, account := range ctx.Accounts() {
		login := ctx.LoginFor(account.Username)
		if login != nil && ctx.Server.SieveEnabled(login.Domain()) {
			accounts = append(accounts, account)
		}
	}
	return accounts
}

func handleCreateFilter(ctx *web.Context) error {
	data, err := filterData(ctx, "")
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "filters/filter-edit", data)
}

func filterData(ctx *web.Context, title string) (*FilterRenderData, error) {
	accounts := sieveAccounts(ctx)
	if len(accounts) == 0 {
		return nil, web.NewError(http.StatusNotFound)
	}
	account := ctx.URLAccount()
	if account == "" {
		account = accounts[0].Username
	}
	data := newFilterRenderData(ctx, title, "")
	data.Accounts, data.Account = accounts, account
	return data, nil
}

// handleImportFilterTooLarge answers a script past the limit on the import form.
func handleImportFilterTooLarge(ctx *web.Context) error {
	data, err := filterData(ctx, ctx.T("filters.import"))
	if err != nil {
		return err
	}
	return web.RenderRefused(ctx, "filters/import", data, ctx.Tf("import.toolarge", maxScriptSize>>20))
}

func handleImportFilter(ctx *web.Context) error {
	data, err := filterData(ctx, ctx.T("filters.import"))
	if err != nil {
		return err
	}
	if ctx.Request().Method != http.MethodPost {
		return ctx.Render(http.StatusOK, "filters/import", data)
	}
	if picked := ctx.FormValue("account"); picked != "" {
		data.Account = picked
	}
	file, err := ctx.FormFile("file")
	if err == nil && file.Size() > maxScriptSize {
		return web.RenderRefused(ctx, "filters/import", data, ctx.Tf("import.toolarge", maxScriptSize>>20))
	}
	if err != nil {
		return web.RenderRefused(ctx, "filters/import", data, ctx.T("form.fileneeded"))
	}
	raw, err := io.ReadAll(file.Open())
	if err != nil {
		return err
	}
	data.Content = strings.ReplaceAll(string(raw), "\r\n", "\n")
	data.Suggested = strings.TrimSuffix(file.Filename, path.Ext(file.Filename))
	return ctx.Render(http.StatusOK, "filters/filter-edit", data)
}

func handleEditFilter(ctx *web.Context) error {
	name := ctx.Param("name")

	var content string
	err := ctx.DoSieve(func(c alborz.SieveClient) error {
		var err error
		content, err = c.GetScript(name)
		return err
	})
	if err != nil {
		return err
	}

	data := newFilterRenderData(ctx, "", "")
	data.Name, data.Content, data.Loaded = name, content, filters.Fingerprint(content)
	return ctx.Render(http.StatusOK, "filters/filter-edit", data)
}

func handleSaveFilter(ctx *web.Context) error {
	name := ctx.FormValue("name")
	content := ctx.FormValue("content")
	if strings.TrimSpace(name) == "" {
		return renderFilterRefusal(ctx, http.StatusUnprocessableEntity, ctx.T("form.nameneeded"), "", content, "")
	}

	// The account choice holds for this request only, like an account-carrying link.
	if account := ctx.FormValue("account"); account != "" && account != ctx.Login.Username() {
		login := ctx.LoginFor(account)
		if login == nil || !ctx.Server.SieveEnabled(login.Domain()) {
			return web.NewError(http.StatusBadRequest, "no filters for that account")
		}
		ctx.Login = login
	}

	var warnings string
	loaded := ctx.FormValue("loaded")
	err := ctx.DoSieve(func(c alborz.SieveClient) error {
		var err error
		warnings, err = filters.SaveScript(c, name, content, loaded)
		return err
	})
	if errors.Is(err, filters.ErrChanged) {
		return renderFilterRefusal(ctx, http.StatusConflict, ctx.T("filters.changed"), name, content, loaded)
	}
	if err != nil {
		// Show the server's rejection next to the script instead of an error page.
		return renderFilterRefusal(ctx, http.StatusUnprocessableEntity, err.Error(), name, content, loaded)
	}

	switch {
	case warnings != "":
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: fmt.Sprintf(ctx.T("notice.filterwarn"), warnings)})
	case loaded == "":
		ctx.Made(ctx.T("notice.filtercreated"), name,
			web.AccountURL(ctx.Path("filters.filter", name), ctx.Login.Username()), web.AccountURL(ctx.Path("filters.create"), ctx.Login.Username()))
	default:
		ctx.PutNotice(ctx.T("notice.filtersaved"))
	}
	return ctx.FoundRedirect(ctx.NextOr(web.AccountURL(ctx.Path("filters.list"), ctx.Login.Username())))
}

func renderFilterRefusal(ctx *web.Context, status int, message, name, content, loaded string) error {
	data := newFilterRenderData(ctx, "", message)
	data.Name, data.Content, data.Loaded = name, content, loaded
	data.Accounts, data.Account = sieveAccounts(ctx), ctx.FormValue("account")
	return ctx.Render(status, "filters/filter-edit", data)
}

func handleActivateFilter(ctx *web.Context) error {
	return filterAction(ctx, "notice.filteron", func(c alborz.SieveClient, name string) error {
		return c.ActivateScript(name)
	})
}

func handleDeactivateFilter(ctx *web.Context) error {
	return answer(ctx, ctx.DoSieve(func(c alborz.SieveClient) error { return c.ActivateScript("") }), "filters.list", ctx.T("notice.filteroff"))
}

func handleDeleteFilter(ctx *web.Context) error {
	return filterAction(ctx, "notice.filterdeleted", func(c alborz.SieveClient, name string) error {
		return c.DeleteScript(name)
	})
}

func filterAction(ctx *web.Context, notice string, action func(alborz.SieveClient, string) error) error {
	name := ctx.Param("name")
	err := ctx.DoSieve(func(c alborz.SieveClient) error { return action(c, name) })
	return answer(ctx, err, "filters.list", ctx.T(notice))
}

// An extension with no entry keeps its bare name: an invented hint is worse than none.
var extensionHints = map[string]string{
	"fileinto":    "sieve.fileinto",
	"reject":      "sieve.reject",
	"ereject":     "sieve.reject",
	"vacation":    "sieve.vacation",
	"imap4flags":  "sieve.imap4flags",
	"envelope":    "sieve.envelope",
	"body":        "sieve.body",
	"regex":       "sieve.regex",
	"relational":  "sieve.relational",
	"subaddress":  "sieve.subaddress",
	"copy":        "sieve.copy",
	"mailbox":     "sieve.mailbox",
	"date":        "sieve.date",
	"variables":   "sieve.variables",
	"include":     "sieve.include",
	"duplicate":   "sieve.duplicate",
	"editheader":  "sieve.editheader",
	"enotify":     "sieve.notify",
	"notify":      "sieve.notify",
	"spamtest":    "sieve.spamtest",
	"virustest":   "sieve.virustest",
	"index":       "sieve.index",
	"environment": "sieve.environment",
}

// Extension is one thing the filter server says a script may use.
type Extension struct {
	Name string
	Hint string
}

func describe(names []string) []Extension {
	out := make([]Extension, 0, len(names))
	for _, n := range names {
		out = append(out, Extension{Name: n, Hint: extensionHints[strings.ToLower(n)]})
	}
	return out
}

const maxScriptSize = 1 << 20
