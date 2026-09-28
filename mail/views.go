package mail

import (
	"slices"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/search"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// KnownView says whether a view name is one alborz offers.
func KnownView(view string) bool {
	return view == "" || view == alborz.ViewStarred || view == alborz.ViewUnread || slices.Contains(alborz.FlagColors[:], view)
}

// StarView says whether a view is a mark the reader put on a message.
func StarView(view string) bool {
	return view == alborz.ViewStarred || slices.Contains(alborz.FlagColors[:], view)
}

// ViewLabel names a view for a page heading.
func ViewLabel(ctx *alborz.Context, view string) string {
	if view == alborz.ViewStarred {
		return ctx.T("mailbox.starred")
	}
	return ctx.T("color." + view)
}

// ViewCriteria is the search a view is, nil for the whole folder.
func ViewCriteria(view string) *imap.SearchCriteria {
	switch view {
	case "":
		return nil
	case alborz.ViewStarred:
		return &imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}}
	case alborz.ViewUnread:
		return &imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}
	}
	add, del := FlagColorFlags(view)
	return &imap.SearchCriteria{Flag: add, NotFlag: del}
}

func listCriteria(c *imapclient.Client, settings *Settings, query, view string) *imap.SearchCriteria {
	criteria := ViewCriteria(view)
	if query != "" {
		criteria = search.And(criteria, search.Parse(query).Criteria(SearchesIndex(c, settings), time.Now()))
	}
	return criteria
}

// SearchesIndex says whether bare terms reach the whole message on this
// connection.
func SearchesIndex(c *imapclient.Client, settings *Settings) bool {
	return c.Caps().Has(imap.CapSearchFuzzy) || settings.IndexedSearch
}
