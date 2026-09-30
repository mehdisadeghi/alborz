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
	web.BaseRenderData
	// Groups holds every account with scripts on a bare URL, else the one the URL names.
	Groups []AccountFilters
	// Rows are the scripts of every group, one page of them.
	Rows     []ScriptRow
	Pager    web.Pager
	Accounts []web.Account
	// Explained is shown once for the page rather than in every server card.
	Explained []web.Explained
	Rail      map[string][]web.RailRow
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
	web.BaseRenderData
	Name      string
	Content   string
	Loaded    string
	Suggested string

	Accounts []web.Account
	Account  string
	Rail     map[string][]web.RailRow
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
				target := ctx.Request().URL.Path + "?account=" + web.QueryValue(ctx.Login.Username())
				return ctx.Redirect(http.StatusFound, target)
			}
			for _, login := range ctx.Logins() {
				if ctx.Server.SieveEnabled(login.Domain()) {
					target := ctx.Request().URL.Path + "?account=" + web.QueryValue(login.Username())
					return ctx.Redirect(http.StatusFound, target)
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

func filterName(ctx *web.Context) (string, error) {
	return ctx.Param("name"), nil
}

func handleListFilters(ctx *web.Context) error {
	data := &FiltersRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("filters.title")),
		Accounts:       sieveAccounts(ctx),
		Rail:           rail(ctx),
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
	accounts := sieveAccounts(ctx)
	if len(accounts) == 0 {
		return web.NewError(http.StatusNotFound)
	}
	account := ctx.URLAccount()
	if account == "" {
		account = accounts[0].Username
	}
	return ctx.Render(http.StatusOK, "filters/filter-edit", &FilterRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx),
		Rail:           rail(ctx),
		Accounts:       accounts,
		Account:        account,
	})
}

func importFilterData(ctx *web.Context) (*FilterRenderData, error) {
	accounts := sieveAccounts(ctx)
	if len(accounts) == 0 {
		return nil, web.NewError(http.StatusNotFound)
	}
	account := ctx.URLAccount()
	if account == "" {
		account = accounts[0].Username
	}
	data := &FilterRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("filters.import")),
		Rail:           rail(ctx),
		Accounts:       accounts,
		Account:        account,
	}
	return data, nil
}

// handleImportFilterTooLarge answers a script past the limit on the import form.
func handleImportFilterTooLarge(ctx *web.Context) error {
	data, err := importFilterData(ctx)
	if err != nil {
		return err
	}
	data.Refused(ctx.Tf("import.toolarge", maxScriptSize>>20))
	return ctx.Render(http.StatusUnprocessableEntity, "filters/import", data)
}

func handleImportFilter(ctx *web.Context) error {
	data, err := importFilterData(ctx)
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
		data.Refused(ctx.Tf("import.toolarge", maxScriptSize>>20))
		return ctx.Render(http.StatusUnprocessableEntity, "filters/import", data)
	}
	if err != nil {
		data.Refused(ctx.T("form.fileneeded"))
		return ctx.Render(http.StatusUnprocessableEntity, "filters/import", data)
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

	return ctx.Render(http.StatusOK, "filters/filter-edit", &FilterRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx),
		Rail:           rail(ctx),
		Name:           name,
		Content:        content,
		Loaded:         filters.Fingerprint(content),
	})
}

func handleSaveFilter(ctx *web.Context) error {
	name := ctx.FormValue("name")
	content := ctx.FormValue("content")
	if strings.TrimSpace(name) == "" {
		return ctx.Render(http.StatusUnprocessableEntity, "filters/filter-edit", &FilterRenderData{
			BaseRenderData: *web.NewBaseRenderData(ctx).Refused(ctx.T("form.nameneeded")),
			Rail:           rail(ctx),
			Content:        content,
			Accounts:       sieveAccounts(ctx),
			Account:        ctx.FormValue("account"),
		})
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
		return ctx.Render(http.StatusConflict, "filters/filter-edit", &FilterRenderData{
			BaseRenderData: *web.NewBaseRenderData(ctx).Refused(ctx.T("filters.changed")),
			Rail:           rail(ctx),
			Name:           name,
			Content:        content,
			Loaded:         loaded,
			Accounts:       sieveAccounts(ctx),
			Account:        ctx.FormValue("account"),
		})
	}
	if err != nil {
		// Show the server's rejection next to the script instead of an error page.
		return ctx.Render(http.StatusUnprocessableEntity, "filters/filter-edit", &FilterRenderData{
			BaseRenderData: *web.NewBaseRenderData(ctx).Refused(err.Error()),
			Rail:           rail(ctx),
			Name:           name,
			Content:        content,
			Loaded:         loaded,
			Accounts:       sieveAccounts(ctx),
			Account:        ctx.FormValue("account"),
		})
	}

	switch {
	case warnings != "":
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: fmt.Sprintf(ctx.T("notice.filterwarn"), warnings)})
	case loaded == "":
		account := "?account=" + web.QueryValue(ctx.Login.Username())
		ctx.Made(ctx.T("notice.filtercreated"), name,
			ctx.Path("filters.filter", name)+account, ctx.Path("filters.create")+account)
	default:
		ctx.PutNotice(ctx.T("notice.filtersaved"))
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.Path("filters.list")+"?account="+web.QueryValue(ctx.Login.Username())))
}

func handleActivateFilter(ctx *web.Context) error {
	name, err := filterName(ctx)
	if err != nil {
		return err
	}

	err = ctx.DoSieve(func(c alborz.SieveClient) error {
		return c.ActivateScript(name)
	})
	return answer(ctx, err, "filters.list", ctx.T("notice.filteron"))
}

func handleDeactivateFilter(ctx *web.Context) error {
	err := ctx.DoSieve(func(c alborz.SieveClient) error {
		return c.ActivateScript("")
	})
	return answer(ctx, err, "filters.list", ctx.T("notice.filteroff"))
}

func handleDeleteFilter(ctx *web.Context) error {
	name, err := filterName(ctx)
	if err != nil {
		return err
	}

	err = ctx.DoSieve(func(c alborz.SieveClient) error {
		return c.DeleteScript(name)
	})
	return answer(ctx, err, "filters.list", ctx.T("notice.filterdeleted"))
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
