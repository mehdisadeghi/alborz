package mail

import (
	"bytes"
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

// Every path either asks for more input or advances past what it returns.
func splitSearchTokens(buf []byte, atEOF bool) (int, []byte, error) {
	start := 0
	for start < len(buf) && buf[start] == ' ' {
		start++
	}
	if start == len(buf) {
		return start, nil, nil
	}
	rest := buf[start:]

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

func wordLen(b []byte) int {
	if i := bytes.IndexByte(b, ' '); i >= 0 {
		return i
	}
	return len(b)
}

func phrased(b []byte) bool {
	return bytes.HasPrefix(b, []byte(`"`)) || bytes.HasPrefix(b, []byte(`-"`))
}

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

// Copies, since bufio.Scanner still owns the buffer.
func unquoted(key, value []byte) []byte {
	token := make([]byte, 0, len(key)+len(value))
	token = append(token, key...)
	return append(token, value...)
}

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
		criteria = searchCriteriaAnd(criteria, ParseQuery(query).Criteria(SearchesIndex(c, settings), time.Now()))
	}
	return criteria
}

// SearchesIndex says whether bare terms reach the whole message on this
// connection.
func SearchesIndex(c *imapclient.Client, settings *Settings) bool {
	return c.Caps().Has(imap.CapSearchFuzzy) || settings.IndexedSearch
}
