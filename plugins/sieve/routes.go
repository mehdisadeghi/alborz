package alborzsieve

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"

	"git.mehdix.org/alborz"
	"github.com/labstack/echo/v4"
	"strings"
)

type FiltersRenderData struct {
	alborz.BaseRenderData
	// Groups is one entry per account shown: every account that holds
	// scripts on a bare URL, the one the URL names otherwise.
	Groups   []AccountFilters
	Accounts []alborz.Account
	// Explained is the union of the groups' extension glossaries,
	// shown once for the page rather than in every server card.
	Explained []alborz.Explained
}

// AccountFilters is one account's scripts and the server they live on.
type AccountFilters struct {
	Account string
	Scripts []alborz.SieveScript
	// Server is what the account's filters are kept on, so the page says
	// who it is talking to rather than leaving it to be guessed.
	Server string
	// Software and Extensions are what that server says it is and what
	// it says it can do, which decides what a filter may use.
	Software   string
	Extensions []Extension
	// Explained is what the named extensions let a filter do, for the
	// disclosure that works where a tooltip does not.
	Explained []alborz.Explained
	// Unreachable is what the filter server said when it would not
	// answer. A provider that is down is not an internal error, and the
	// page says so itself instead of becoming a status page.
	Unreachable string
}

type FilterRenderData struct {
	alborz.BaseRenderData
	Name    string
	Content string
	// Loaded fingerprints the script as the editor received it, so a
	// save can tell whether the server still holds that version.
	Loaded string
	Error  string

	// Accounts that can hold a script, for the create form's
	// destination, and Account the one it opens on: the URL's, or the
	// first that holds scripts. Empty when editing, where the script's
	// own account is already settled.
	Accounts []alborz.Account
	Account  string
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
				target := ctx.Request().URL.Path + "?account=" + alborz.AddressParam(ctx.Session.Username())
				return ctx.Redirect(http.StatusFound, target)
			}
			for _, session := range ctx.Sessions() {
				if ctx.Server.SieveEnabled(session.Domain()) {
					target := ctx.Request().URL.Path + "?account=" + alborz.AddressParam(session.Username())
					return ctx.Redirect(http.StatusFound, target)
				}
			}
			return echo.ErrNotFound
		}
	}
	p.GET("/filters", handleListFilters)
	p.GET("/filters/create", handleCreateFilter)
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
	}
	unreachable := 0
	for _, account := range data.Accounts {
		if ctx.URLAccount() != "" && account.Username != ctx.URLAccount() {
			continue
		}
		session := ctx.SessionFor(account.Username)
		group := AccountFilters{Account: account.Username, Server: ctx.Server.SieveHost(session.Domain())}
		err := session.DoSieve(func(c alborz.SieveClient) error {
			var err error
			group.Scripts, err = c.ListScripts()
			group.Software = c.Implementation()
			group.Extensions = describe(c.Extensions())
			for _, e := range group.Extensions {
				if e.Hint != "" {
					group.Explained = append(group.Explained,
						alborz.Explained{Term: e.Name, Hint: ctx.T(e.Hint)})
				}
			}
			return err
		})
		if err != nil {
			// The filters live on somebody else's machine, and it being
			// unreachable is news about that machine rather than a fault
			// here. The page says which machine and what it said.
			ctx.Logger().Printf("failed to list sieve scripts on %s: %v", group.Server, err)
			group.Unreachable = err.Error()
			unreachable++
		}
		data.Groups = append(data.Groups, group)
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
	return ctx.Render(status, "filters.html", data)
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
	accounts := sieveAccounts(ctx)
	if len(accounts) == 0 {
		return echo.ErrNotFound
	}
	account := ctx.URLAccount()
	if account == "" {
		account = accounts[0].Username
	}
	return ctx.Render(http.StatusOK, "filter-edit.html", &FilterRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx),
		Accounts:       accounts,
		Account:        account,
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
		Loaded:         fingerprint(content),
	})
}

// fingerprint names a script's content. ManageSieve keeps no version
// or date for a script, so the content itself is what a save compares
// against to notice an edit made elsewhere in the meantime.
func fingerprint(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
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
			Account:        ctx.FormValue("account"),
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
	changed := false
	loaded := ctx.FormValue("loaded")
	err := ctx.DoSieve(func(c alborz.SieveClient) error {
		// An edit is checked against what the server holds now: a
		// change made elsewhere since the editor opened is not
		// overwritten but reported, and the reader starts over from
		// the current script.
		if loaded != "" {
			current, err := c.GetScript(name)
			if err != nil {
				return err
			}
			if fingerprint(current) != loaded {
				changed = true
				return nil
			}
		}
		var err error
		warnings, err = c.PutScript(name, content)
		return err
	})
	if err == nil && changed {
		return ctx.Render(http.StatusConflict, "filter-edit.html", &FilterRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx),
			Name:           name,
			Content:        content,
			Loaded:         loaded,
			Error:          ctx.T("filters.changed"),
			Accounts:       sieveAccounts(ctx),
			Account:        ctx.FormValue("account"),
		})
	}
	if err != nil {
		// The server rejects invalid scripts; show the reason next to
		// the script instead of an error page.
		return ctx.Render(http.StatusUnprocessableEntity, "filter-edit.html", &FilterRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx),
			Name:           name,
			Content:        content,
			Loaded:         loaded,
			Error:          err.Error(),
			Accounts:       sieveAccounts(ctx),
			Account:        ctx.FormValue("account"),
		})
	}

	if warnings != "" {
		ctx.Session.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: fmt.Sprintf(ctx.T("notice.filterwarn"), warnings)})
	} else {
		ctx.Session.PutNotice(ctx.T("notice.filtersaved"))
	}
	return ctx.Redirect(http.StatusFound, "/filters?account="+alborz.AddressParam(ctx.Session.Username()))
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

// extensionHints names the Sieve extensions worth explaining, by what
// they let a filter do. An extension with no entry keeps its name and
// no explanation: inventing one would be worse than saying nothing.
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
	Hint string // translation key, empty where alborz has nothing to add
}

// describe pairs each advertised extension with what it means.
func describe(names []string) []Extension {
	out := make([]Extension, 0, len(names))
	for _, n := range names {
		out = append(out, Extension{Name: n, Hint: extensionHints[strings.ToLower(n)]})
	}
	return out
}
