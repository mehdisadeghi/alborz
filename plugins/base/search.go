package alborzbase

import (
	"bufio"
	"bytes"
	"slices"
	"strings"

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
	// is one run of bare text.
	term := -1
	for i := 0; i < len(rest); {
		word := i + wordLen(rest[i:])
		if bytes.IndexByte(rest[i:word], ':') >= 0 {
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

// unquoted joins a term's key to its value without copying over the
// caller's buffer, which bufio.Scanner still owns.
func unquoted(key, value []byte) []byte {
	token := make([]byte, 0, len(key)+len(value))
	token = append(token, key...)
	return append(token, value...)
}

// Matching is the server's; what is alborz's is the question. A bare
// term searches From, To, Cc and Subject, and everything - TEXT, the
// whole message - only on a server that says it holds an index: Dovecot
// advertises SEARCH=FUZZY (RFC 6203) exactly when its full-text plugin
// is loaded, and without an index TEXT over a large folder is a scan of
// minutes. text: asks for the whole message regardless, which is what
// the results page offers when it searched headers only; from:,
// subject: and friends narrow the field, and body: is the body alone.
func PrepareSearch(terms string, indexed bool) *imap.SearchCriteria {
	var criteria *imap.SearchCriteria

	scanner := bufio.NewScanner(strings.NewReader(terms))
	scanner.Split(splitSearchTokens)

	for scanner.Scan() {
		term := scanner.Text()
		if !strings.ContainsRune(term, ':') {
			if indexed {
				criteria = searchCriteriaAnd(
					criteria, &imap.SearchCriteria{Text: []string{term}})
			} else {
				criteria = searchCriteriaAnd(
					criteria,
					searchCriteriaOr(
						searchCriteriaHeader("From", term),
						searchCriteriaHeader("To", term),
						searchCriteriaHeader("Cc", term),
						searchCriteriaHeader("Subject", term),
					),
				)
			}
		} else {
			parts := strings.SplitN(term, ":", 2)
			key, value := parts[0], parts[1]
			switch strings.ToLower(key) {
			case "from":
				criteria = searchCriteriaAnd(
					criteria, searchCriteriaHeader("From", value))
			case "to":
				criteria = searchCriteriaAnd(
					criteria, searchCriteriaHeader("To", value))
			case "cc":
				criteria = searchCriteriaAnd(
					criteria, searchCriteriaHeader("Cc", value))
			case "subject":
				criteria = searchCriteriaAnd(
					criteria, searchCriteriaHeader("Subject", value))
			case "list":
				criteria = searchCriteriaAnd(
					criteria, searchCriteriaHeader("List-Id", value))
			case "body":
				criteria = searchCriteriaAnd(
					criteria, &imap.SearchCriteria{Body: []string{value}})
			case "text":
				criteria = searchCriteriaAnd(
					criteria, &imap.SearchCriteria{Text: []string{value}})
			default:
				continue
			}
		}
	}

	// A term nobody recognises is skipped above, so a query made only of
	// those narrows by nothing. That is empty criteria, not no criteria:
	// the search path takes a pointer and a nil one is a crash.
	if criteria == nil {
		criteria = &imap.SearchCriteria{}
	}
	return criteria
}

// A view is a folder narrowed by a flag: the starred, the unread, the
// stars of one colour. It is a search with a name and costs nothing
// more than one.
const (
	ViewStarred = "starred"
	ViewUnread  = "unread"
)

// KnownView says whether a view name is one alborz offers; a colour
// counts, since FlagColors is where the names come from.
func KnownView(view string) bool {
	return view == "" || view == ViewStarred || view == ViewUnread || slices.Contains(FlagColors[:], view)
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

// SearchesIndex says whether bare terms reach the whole message on this
// connection, and SearchesText whether the query asks for it anyway.
func SearchesIndex(c *imapclient.Client, settings *Settings) bool {
	return c.Caps().Has(imap.CapSearchFuzzy) || settings.IndexedSearch
}

func SearchesText(terms string) bool {
	return slices.ContainsFunc(searchTokens(terms), func(t string) bool {
		return strings.HasPrefix(strings.ToLower(t), "text:")
	})
}

// TextQuery is the query with every bare term asked of the whole
// message: the link a results page offers when it searched headers.
// Empty when there is no bare term to widen.
func TextQuery(terms string) string {
	var out []string
	widened := false
	for _, t := range searchTokens(terms) {
		if !strings.ContainsRune(t, ':') {
			// A bare run of words is one phrase; quoted, it stays one.
			if strings.ContainsRune(t, ' ') {
				t = `"` + t + `"`
			}
			t, widened = "text:"+t, true
		}
		out = append(out, t)
	}
	if !widened {
		return ""
	}
	return strings.Join(out, " ")
}

func searchTokens(terms string) []string {
	scanner := bufio.NewScanner(strings.NewReader(terms))
	scanner.Split(splitSearchTokens)
	var out []string
	for scanner.Scan() {
		out = append(out, scanner.Text())
	}
	return out
}
