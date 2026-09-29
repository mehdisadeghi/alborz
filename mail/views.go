package mail

import (
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz/mail/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// KnownView says whether a view name is one alborz offers.
func KnownView(view string) bool {
	return view == "" || view == web.ViewStarred || view == web.ViewUnread || slices.Contains(web.FlagColors[:], view)
}

// StarView says whether a view is a mark the reader put on a message.
func StarView(view string) bool {
	return view == web.ViewStarred || slices.Contains(web.FlagColors[:], view)
}

// ViewLabel names a view for a page heading.
func ViewLabel(ctx *web.Context, view string) string {
	if view == web.ViewStarred {
		return ctx.T("mailbox.starred")
	}
	return ctx.T("color." + view)
}

// ViewCriteria is the search a view is, nil for the whole folder.
func ViewCriteria(view string) *imap.SearchCriteria {
	switch view {
	case "":
		return nil
	case web.ViewStarred:
		return &imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}}
	case web.ViewUnread:
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

// queryDays names the days a query's dates resolve to at now: newer_than:
// moves with the day, so a list held since yesterday is another list.
func queryDays(query string, now time.Time) string {
	if query == "" {
		return ""
	}
	var days []string
	var walk func(c *imap.SearchCriteria)
	walk = func(c *imap.SearchCriteria) {
		for _, t := range []time.Time{c.Since, c.Before, c.SentSince, c.SentBefore} {
			if !t.IsZero() {
				days = append(days, t.Format(time.DateOnly))
			}
		}
		for i := range c.Not {
			walk(&c.Not[i])
		}
		for i := range c.Or {
			walk(&c.Or[i][0])
			walk(&c.Or[i][1])
		}
	}
	if c := search.Parse(query).Criteria(false, now); c != nil {
		walk(c)
	}
	return strings.Join(days, ",")
}

// SearchesIndex says whether bare terms reach the whole message on this
// connection.
func SearchesIndex(c *imapclient.Client, settings *Settings) bool {
	return c.Caps().Has(imap.CapSearchFuzzy) || settings.IndexedSearch
}
