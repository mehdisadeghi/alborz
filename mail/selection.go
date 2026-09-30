package mail

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// A bulk form's "everything" means the whole list its next URL names, asked again.

func wholeView(form url.Values) (query, view string, whole bool) {
	if form.Get("everything") == "" {
		return "", "", false
	}
	next, err := url.Parse(form.Get("next"))
	// A conversation is decided by THREAD; asking it again here would duplicate that page.
	if err != nil || next.Query().Get("thread") != "" {
		return "", "", false
	}
	view = next.Query().Get("view")
	if !KnownView(view) {
		view = ""
	}
	return next.Query().Get("query"), view, true
}

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
	// The list drops what the envelope contradicts (carried), so the selection must too.
	q := search.Parse(query)
	if len(uids) == 0 || !q.Addressed() {
		return uids, nil
	}
	kept, err := carried(c, q, imap.UIDSetNum(uids...))
	if err != nil {
		return nil, err
	}
	return slices.Collect(maps.Keys(kept)), nil
}

func selection(ctx *web.Context, mboxName string, form url.Values) ([]imap.UID, error) {
	query, view, whole := wholeView(form)
	if !whole {
		uids, err := parseUidList(form["uids"])
		if err != nil {
			return nil, web.NewError(http.StatusBadRequest, err)
		}
		return uids, nil
	}
	return folderViewUIDs(ctx.Request().Context(), ctx.Login, mboxName, query, view)
}

func folderSelection(ctx *web.Context) (mboxName string, form url.Values, uids []imap.UID, err error) {
	if mboxName, err = mailboxRef(ctx); err != nil {
		return "", nil, nil, err
	}
	if form, err = ctx.FormParams(); err != nil {
		return "", nil, nil, web.NewError(http.StatusBadRequest, err)
	}
	uids, err = selection(ctx, mboxName, form)
	return mboxName, form, uids, err
}

// folderViewUIDs lists what a folder's view holds, off the connection the reader's clicks wait on.
func folderViewUIDs(ctx context.Context, login *alborz.Login, mboxName, query, view string) ([]imap.UID, error) {
	settings, err := LoadSettings(ctx, login.Store())
	if err != nil {
		return nil, err
	}
	var uids []imap.UID
	err = login.DoIMAPWork(ctx, alborz.IMAPScan, alborz.ScanTimeout, func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}
		var err error
		uids, err = viewUIDs(c, settings, query, view)
		return err
	})
	return uids, err
}

func (p *plugin) mergedSelection(ctx *web.Context, role string, form url.Values) ([]rowRef, error) {
	query, view, whole := wholeView(form)
	if !whole {
		refs, err := parseRefs(form["refs"])
		if err != nil {
			return nil, web.NewError(http.StatusBadRequest, err)
		}
		return refs, nil
	}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return nil, err
	}
	next, _ := url.Parse(form.Get("next"))
	spanning := strings.TrimSuffix(next.Path, "/") == "/search"
	logins := ctx.Logins()
	if account := next.Query().Get("account"); account != "" {
		s := ctx.LoginFor(account)
		if s == nil {
			return nil, web.NewError(http.StatusBadRequest, "no such account")
		}
		logins = []*alborz.Login{s}
	}
	var refs []rowRef
	for _, s := range logins {
		found, err := p.viewRefs(ctx.Request().Context(), s, role, spanning, settings, query, view)
		if err != nil {
			return nil, err
		}
		refs = append(refs, found...)
	}
	return refs, nil
}

// viewRefs are what a merged view holds on one account: its role's folder, or every folder a search spans.
func (p *plugin) viewRefs(ctx context.Context, s *alborz.Login, role string, spanning bool, settings *Settings, query, view string) ([]rowRef, error) {
	var folders []string
	if spanning {
		sb, err := p.sidebarFor(s)
		if err != nil {
			return nil, err
		}
		folders = searchFolders(sb.mailboxes, search.Parse(query))
	}
	var refs []rowRef
	err := s.DoIMAPWork(ctx, alborz.IMAPScan, alborz.ScanTimeout, func(c *imapclient.Client) error {
		if !spanning {
			folder, err := p.resolveRole(c, s.Username(), role)
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
	return refs, err

}
