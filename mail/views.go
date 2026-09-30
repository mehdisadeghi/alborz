package mail

import (
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/imapsearch"
	"git.mehdix.org/alborz/search"
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
		criteria = imapsearch.And(criteria, imapsearch.Of(search.Parse(query), SearchesIndex(c, settings), time.Now()))
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
	if c := imapsearch.Of(search.Parse(query), false, now); c != nil {
		walk(c)
	}
	return strings.Join(days, ",")
}

// SearchesIndex says whether bare terms reach the whole message on this
// connection.
func SearchesIndex(c *imapclient.Client, settings *Settings) bool {
	return c.Caps().Has(imap.CapSearchFuzzy) || settings.IndexedSearch
}
