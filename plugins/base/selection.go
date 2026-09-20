package alborzbase

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/labstack/echo/v4"
)

// A page holds some of a list. A bulk form that carries "everything"
// means the list the page was cut from - the folder, its search or its
// view - and names it by the address it returns to, the one place a
// form has the list's own parameters. The rows ticked are then beside
// the point: the server asks the view again and acts on what it finds.

// wholeView reports whether the form asks for more than its rows, and
// the search that the view it came from is.
func wholeView(form url.Values) (query, view string, whole bool) {
	if form.Get("everything") == "" {
		return "", "", false
	}
	next, err := url.Parse(form.Get("next"))
	// A conversation is a list too, and one THREAD decides: asking it
	// again here would be a second implementation of that page.
	if err != nil || next.Query().Get("thread") != "" {
		return "", "", false
	}
	view = next.Query().Get("view")
	if !KnownView(view) {
		view = ""
	}
	return next.Query().Get("query"), view, true
}

// viewUIDs asks a selected folder for everything a view of it holds.
func viewUIDs(c *imapclient.Client, settings *Settings, query, view string) ([]imap.UID, error) {
	criteria := listCriteria(c, settings, query, view)
	if criteria == nil {
		criteria = &imap.SearchCriteria{}
	}
	data, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, err
	}
	uids := data.AllUIDs()
	// The list drops what the envelope contradicts (carried), so
	// everything in it cannot mean the rows it never showed.
	q := ParseQuery(query)
	if len(uids) == 0 || !q.Addressed() {
		return uids, nil
	}
	kept, err := carried(c, q, imap.UIDSetNum(uids...))
	if err != nil {
		return nil, err
	}
	return slices.Collect(maps.Values(kept)), nil
}

// selection is what a bulk action on one folder applies to: the rows
// ticked, or the whole view they were a page of.
func selection(ctx *alborz.Context, mboxName string, form url.Values) ([]imap.UID, error) {
	query, view, whole := wholeView(form)
	if !whole {
		uids, err := parseUidList(form["uids"])
		if err != nil {
			return nil, echo.NewHTTPError(http.StatusBadRequest, err)
		}
		return uids, nil
	}
	return folderViewUIDs(ctx, mboxName, query, view)
}

// folderSelection reads what a folder's action route is posted: the
// folder it names, the form, and the selection the form makes of it.
func folderSelection(ctx *alborz.Context) (mboxName string, form url.Values, uids []imap.UID, err error) {
	if mboxName, err = mailboxRef(ctx); err != nil {
		return "", nil, nil, err
	}
	if form, err = ctx.FormParams(); err != nil {
		return "", nil, nil, echo.NewHTTPError(http.StatusBadRequest, err)
	}
	uids, err = selection(ctx, mboxName, form)
	return mboxName, form, uids, err
}

// folderViewUIDs is everything a view of one folder holds.
func folderViewUIDs(ctx *alborz.Context, mboxName, query, view string) ([]imap.UID, error) {
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return nil, err
	}
	var uids []imap.UID
	err = ctx.DoIMAPScan(func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}
		var err error
		uids, err = viewUIDs(c, settings, query, view)
		return err
	})
	return uids, err
}

// mergedSelection is the same for a list whose rows are several
// folders': the role's folder in every account, or for a search across
// folders every folder the query reaches.
func mergedSelection(ctx *alborz.Context, role string, form url.Values) ([]rowRef, error) {
	query, view, whole := wholeView(form)
	if !whole {
		refs, err := parseRefs(form["refs"])
		if err != nil {
			return nil, echo.NewHTTPError(http.StatusBadRequest, err)
		}
		return refs, nil
	}
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return nil, err
	}
	next, _ := url.Parse(form.Get("next"))
	spanning := strings.TrimSuffix(next.Path, "/") == "/search"
	sessions := ctx.Sessions()
	if account := next.Query().Get("account"); account != "" {
		s := ctx.SessionFor(account)
		if s == nil {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "no such account")
		}
		sessions = []*alborz.Session{s}
	}
	var refs []rowRef
	for _, s := range sessions {
		var folders []string
		if spanning {
			sb, err := sidebarFor(s)
			if err != nil {
				return nil, err
			}
			folders = searchFolders(sb.mailboxes, ParseQuery(query))
		}
		err := s.DoIMAPWork(ctx.Request().Context(), alborz.IMAPScan, alborz.ScanTimeout, func(c *imapclient.Client) error {
			if !spanning {
				folder, err := resolveRole(c, s.Username(), role)
				if err != nil || folder == "" {
					return err
				}
				folders = []string{folder}
			}
			for _, folder := range folders {
				if err := ensureMailboxSelected(c, folder); err != nil {
					return err
				}
				uids, err := viewUIDs(c, settings, query, view)
				if err != nil {
					return err
				}
				for _, uid := range uids {
					refs = append(refs, rowRef{account: s.Username(), mailbox: folder, uid: uid})
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return refs, nil
}
