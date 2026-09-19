package alborzbase

import (
	"bytes"
	"fmt"
	"net/url"
	"slices"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func searchCriteriaHeader(k, v string) *imap.SearchCriteria {
	return &imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{
			{Key: k, Value: v},
		},
	}
}

func searchCriteriaOr(criteria ...*imap.SearchCriteria) *imap.SearchCriteria {
	if criteria[0] == nil {
		criteria = criteria[1:]
	}
	or := criteria[0]
	for _, c := range criteria[1:] {
		or = &imap.SearchCriteria{
			Or: [][2]imap.SearchCriteria{{*or, *c}},
		}
	}
	return or
}

func searchCriteriaAnd(criteria ...*imap.SearchCriteria) *imap.SearchCriteria {
	if criteria[0] == nil {
		criteria = criteria[1:]
	}
	and := criteria[0]
	for _, c := range criteria[1:] {
		and.And(c)
	}
	return and
}

// splitSearchTokens splits a query into runs of bare text and key:value
// terms, the value optionally quoted:
//
//	hello world foo:bar baz trains:"are cool"
//	-> "hello world", "foo:bar", "baz", "trains:are cool"
//
// Every path either asks for more input or advances past what it
// returns. Handing bufio.Scanner a token with no advance leaves it
// returning that same token forever: an unquoted term ("from:a@b.com",
// which is what the search box invites) used to spin here inside the
// IMAP lock, taking the account's other requests down with it.
func splitSearchTokens(buf []byte, atEOF bool) (int, []byte, error) {
	start := 0
	for start < len(buf) && buf[start] == ' ' {
		start++
	}
	if start == len(buf) {
		return start, nil, nil
	}
	rest := buf[start:]

	// The first word carrying a colon opens a term; whatever precedes it
	// is one run of bare text. A quoted phrase is text whatever it holds.
	term := -1
	for i := 0; i < len(rest); {
		word := i + bareLen(rest[i:])
		if !phrased(rest[i:]) && bytes.IndexByte(rest[i:word], ':') >= 0 {
			term = i
			break
		}
		for i = word; i < len(rest) && rest[i] == ' '; i++ {
		}
	}
	switch {
	case term > 0:
		return start + term, bytes.TrimRight(rest[:term], " "), nil
	case term < 0 && !atEOF:
		return start, nil, nil
	case term < 0:
		return len(buf), bytes.TrimRight(rest, " "), nil
	}

	colon := bytes.IndexByte(rest, ':')
	value := rest[colon+1:]
	if len(value) > 0 && value[0] == '"' {
		closing := bytes.IndexByte(value[1:], '"')
		if closing < 0 && !atEOF {
			return start, nil, nil
		}
		if closing < 0 {
			// Unclosed at the end of the query: take it as written.
			return len(buf), unquoted(rest[:colon+1], value[1:]), nil
		}
		return start + colon + closing + 3, unquoted(rest[:colon+1], value[1:closing+1]), nil
	}
	word := colon + 1 + wordLen(value)
	if word == len(rest) && !atEOF {
		return start, nil, nil
	}
	return start + word, rest[:word], nil
}

// wordLen is the length of b up to its first space, or all of it.
func wordLen(b []byte) int {
	if i := bytes.IndexByte(b, ' '); i >= 0 {
		return i
	}
	return len(b)
}

// phrased says whether bare text opens with a quoted phrase, negated or
// not.
func phrased(b []byte) bool {
	return bytes.HasPrefix(b, []byte(`"`)) || bytes.HasPrefix(b, []byte(`-"`))
}

// bareLen is wordLen in a run of bare text, where a quoted phrase is
// one word with its spaces, and an unclosed one runs to the end.
func bareLen(b []byte) int {
	if !phrased(b) {
		return wordLen(b)
	}
	open := bytes.IndexByte(b, '"')
	closing := bytes.IndexByte(b[open+1:], '"')
	if closing < 0 {
		return len(b)
	}
	end := open + closing + 2
	return end + wordLen(b[end:])
}

// unquoted joins a term's key to its value without copying over the
// caller's buffer, which bufio.Scanner still owns.
func unquoted(key, value []byte) []byte {
	token := make([]byte, 0, len(key)+len(value))
	token = append(token, key...)
	return append(token, value...)
}

// A view is a folder narrowed by a flag: the starred, the unread, the
// stars of one colour. It is a search with a name and costs nothing
// more than one.
const (
	ViewStarred = "starred"
	ViewUnread  = "unread"
)

// StarMatches says whether a mark answers a view: starred is any
// colour, a colour is itself, no view is everything.
func StarMatches(star, view string) bool {
	switch view {
	case "":
		return true
	case ViewStarred:
		return star != ""
	}
	return star == view
}

// StarLeaves says a row whose star is now star leaves the list at next:
// the list is narrowed to a star, and this one no longer answers it.
func StarLeaves(next, star string) bool {
	u, err := url.Parse(next)
	if err != nil {
		return false
	}
	view := u.Query().Get("view")
	return (view == ViewStarred || slices.Contains(FlagColors[:], view)) && !StarMatches(star, view)
}

// StarNotice says what became of a row's star that left its list, with
// the way back: the same action with the colour it had.
func StarNotice(ctx *alborz.Context, star, action string, undo url.Values) alborz.Notice {
	text := ctx.T("notice.starremoved")
	if star != "" {
		text = fmt.Sprintf(ctx.T("notice.starchanged"), ctx.T("color."+star))
	}
	return alborz.Notice{Kind: alborz.NoticeDone, Text: text, Action: ctx.Undo(action, undo)}
}

// KnownView says whether a view name is one alborz offers; a colour
// counts, since FlagColors is where the names come from.
func KnownView(view string) bool {
	return view == "" || view == ViewStarred || view == ViewUnread || slices.Contains(FlagColors[:], view)
}

// StarView says whether a view is a mark the reader put on a message:
// the star or one of its colours. A mark travels with the message, so
// the view that asks for it looks wherever the message may have been
// filed, while unread is the folder's own state and stays in it.
func StarView(view string) bool {
	return view == ViewStarred || slices.Contains(FlagColors[:], view)
}

// ViewLabel names a view for a page heading.
func ViewLabel(ctx *alborz.Context, view string) string {
	if view == ViewStarred {
		return ctx.T("mailbox.starred")
	}
	return ctx.T("color." + view)
}

// ViewRows are the views a list's filter menu offers: unread where the
// list has a read state, starred, and the seven colours. The one in
// force links to clear, the page the caller names, since an agenda
// cleared of its star is still the agenda.
func ViewRows(ctx *alborz.Context, current string, unread bool, clear string) []alborz.FilterRow {
	row := func(label, view, star string) alborz.FilterRow {
		href := ctx.WithParam("view", view)
		if view == current {
			href = clear
		}
		return alborz.FilterRow{Label: label, Href: href, Current: view == current, Star: star}
	}
	var rows []alborz.FilterRow
	if unread {
		rows = append(rows, row(ctx.T("mailbox.unread"), ViewUnread, ""))
	}
	rows = append(rows, row(ctx.T("mailbox.starred"), ViewStarred, ""))
	for _, name := range FlagColors {
		rows = append(rows, row(ctx.T("color."+name), name, name))
	}
	return rows
}

// ViewCriteria is the search a view is, nil for the whole folder.
func ViewCriteria(view string) *imap.SearchCriteria {
	switch view {
	case "":
		return nil
	case ViewStarred:
		return &imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}}
	case ViewUnread:
		return &imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}
	}
	add, del := FlagColorFlags(view)
	return &imap.SearchCriteria{Flag: add, NotFlag: del}
}

// listCriteria is the search a list is: its query and its view, both in
// force when both are named, since a reader who narrowed to a sender
// and then asked for the starred ones means both. nil for the whole
// folder.
func listCriteria(c *imapclient.Client, settings *Settings, query, view string) *imap.SearchCriteria {
	criteria := ViewCriteria(view)
	if query != "" {
		criteria = searchCriteriaAnd(criteria, ParseQuery(query).Criteria(SearchesIndex(c, settings), time.Now()))
	}
	return criteria
}

// SearchesIndex says whether bare terms reach the whole message on this
// connection.
func SearchesIndex(c *imapclient.Client, settings *Settings) bool {
	return c.Caps().Has(imap.CapSearchFuzzy) || settings.IndexedSearch
}
