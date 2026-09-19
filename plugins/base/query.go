package alborzbase

import (
	"bufio"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

// A Query is a search as the reader typed it, parsed once. The language
// is the part of Gmail's that IMAP can answer by itself: a bare run of
// words, key:value terms with the value optionally quoted, and a minus
// before a term for its opposite.
//
//	invoice from:hetzner -is:read after:2026-01-01 larger:1M in:archive
//
// Matching is the server's; what is alborz's is the question. What IMAP
// has no criterion for is left out rather than guessed at: there is no
// has:attachment, because SEARCH cannot see a message's structure and a
// Content-Type test would call every signed mail an attachment.
type Query struct {
	terms []queryTerm
}

// queryTerm is one term: a bare run of words when key is empty.
type queryTerm struct {
	key, value string
	not        bool
	// quoted keeps a bare phrase one term when the query is written
	// out again, beside the bare words it stood among.
	quoted bool
}

// queryScope is where in: points, which is not a property of a message
// and so no part of the criteria.
const queryScope = "in"

// Everywhere is the in: value that takes a search to the folders it
// otherwise leaves out, Junk and Trash, as it does in Gmail.
const Everywhere = "anywhere"

// An operator turns a value into criteria, or into nothing when the
// value is not one it understands.
type operator func(value string, now time.Time) *imap.SearchCriteria

func headerOperator(field string) operator {
	return func(value string, _ time.Time) *imap.SearchCriteria {
		return searchCriteriaHeader(field, value)
	}
}

var operators = map[string]operator{
	"from":    headerOperator("From"),
	"to":      headerOperator("To"),
	"cc":      headerOperator("Cc"),
	"bcc":     headerOperator("Bcc"),
	"subject": headerOperator("Subject"),
	"list":    headerOperator("List-Id"),
	"body": func(value string, _ time.Time) *imap.SearchCriteria {
		return &imap.SearchCriteria{Body: []string{value}}
	},
	"text": func(value string, _ time.Time) *imap.SearchCriteria {
		return &imap.SearchCriteria{Text: []string{value}}
	},
	"is": func(value string, _ time.Time) *imap.SearchCriteria {
		state, ok := messageStates[strings.ToLower(value)]
		if !ok {
			return nil
		}
		if state.not {
			return &imap.SearchCriteria{NotFlag: []imap.Flag{state.flag}}
		}
		return &imap.SearchCriteria{Flag: []imap.Flag{state.flag}}
	},
	// SINCE takes the day itself and BEFORE stops short of it (RFC 3501
	// 6.4.4), which is how Gmail reads after: and before: too.
	"after": func(value string, _ time.Time) *imap.SearchCriteria {
		day, ok := queryDay(value)
		if !ok {
			return nil
		}
		return &imap.SearchCriteria{Since: day}
	},
	"before": func(value string, _ time.Time) *imap.SearchCriteria {
		day, ok := queryDay(value)
		if !ok {
			return nil
		}
		return &imap.SearchCriteria{Before: day}
	},
	"newer_than": func(value string, now time.Time) *imap.SearchCriteria {
		day, ok := queryAgo(value, now)
		if !ok {
			return nil
		}
		return &imap.SearchCriteria{Since: day}
	},
	"older_than": func(value string, now time.Time) *imap.SearchCriteria {
		day, ok := queryAgo(value, now)
		if !ok {
			return nil
		}
		return &imap.SearchCriteria{Before: day}
	},
	"larger": func(value string, _ time.Time) *imap.SearchCriteria {
		size, ok := querySize(value)
		if !ok {
			return nil
		}
		return &imap.SearchCriteria{Larger: size}
	},
	"smaller": func(value string, _ time.Time) *imap.SearchCriteria {
		size, ok := querySize(value)
		if !ok {
			return nil
		}
		return &imap.SearchCriteria{Smaller: size}
	},
}

// messageStates are what is: asks about, each a flag or its absence.
var messageStates = map[string]struct {
	flag imap.Flag
	not  bool
}{
	"unread":    {imap.FlagSeen, true},
	"read":      {imap.FlagSeen, false},
	"starred":   {imap.FlagFlagged, false},
	"flagged":   {imap.FlagFlagged, false},
	"unstarred": {imap.FlagFlagged, true},
	"answered":  {imap.FlagAnswered, false},
	"replied":   {imap.FlagAnswered, false},
	"draft":     {imap.FlagDraft, false},
}

// queryDayLayouts are the two ways a day is written in a query: ISO, and
// Gmail's own with slashes.
var queryDayLayouts = []string{"2006-01-02", "2006/01/02"}

func queryDay(value string) (time.Time, bool) {
	for _, layout := range queryDayLayouts {
		if day, err := time.Parse(layout, value); err == nil {
			return day, true
		}
	}
	return time.Time{}, false
}

// queryAgo reads Gmail's spans - 3d, 2w, 6m, 1y - back from now.
func queryAgo(value string, now time.Time) (time.Time, bool) {
	if len(value) < 2 {
		return time.Time{}, false
	}
	n, err := strconv.Atoi(value[:len(value)-1])
	if err != nil || n <= 0 {
		return time.Time{}, false
	}
	switch value[len(value)-1] {
	case 'd':
		return now.AddDate(0, 0, -n), true
	case 'w':
		return now.AddDate(0, 0, -7*n), true
	case 'm':
		return now.AddDate(0, -n, 0), true
	case 'y':
		return now.AddDate(-n, 0, 0), true
	}
	return time.Time{}, false
}

// querySize reads a size in bytes, or with Gmail's K and M.
func querySize(value string) (int64, bool) {
	unit := int64(1)
	switch {
	case strings.HasSuffix(strings.ToLower(value), "k"):
		unit, value = 1<<10, value[:len(value)-1]
	case strings.HasSuffix(strings.ToLower(value), "m"):
		unit, value = 1<<20, value[:len(value)-1]
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * unit, true
}

// ParseQuery reads a query. A key nobody knows is not an operator, and
// neither is a known one with a value it cannot read: either is searched
// for as the text it is, colon and all. Dropping such a term would
// answer after:yesterday with every message in the folder, which reads
// as a result.
func ParseQuery(raw string) Query {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Split(splitSearchTokens)
	var q Query
	for scanner.Scan() {
		token := scanner.Text()
		key, value, keyed := strings.Cut(token, ":")
		not := strings.HasPrefix(key, "-")
		key = strings.ToLower(strings.TrimPrefix(key, "-"))
		read, known := operators[key]
		switch {
		case phrased([]byte(token)) || !keyed:
			q.terms = append(q.terms, bareTerms(token)...)
		case value == "" || (key != queryScope && (!known || read(value, time.Time{}) == nil)):
			q.terms = append(q.terms, queryTerm{value: token})
		default:
			q.terms = append(q.terms, queryTerm{key: key, value: value, not: not})
		}
	}
	return q
}

// bareTerms reads a run of bare text: its words are one term, as they
// were typed, and a quoted phrase or a word behind a minus is a term
// of its own.
func bareTerms(run string) []queryTerm {
	var terms []queryTerm
	var words []string
	flush := func() {
		if len(words) > 0 {
			terms = append(terms, queryTerm{value: strings.Join(words, " ")})
			words = nil
		}
	}
	for run = strings.TrimLeft(run, " "); run != ""; run = strings.TrimLeft(run, " ") {
		word := run[:bareLen([]byte(run))]
		run = run[len(word):]
		value, not := strings.CutPrefix(word, "-")
		quoted := strings.HasPrefix(value, `"`)
		value = strings.ReplaceAll(value, `"`, "")
		if value == "" || !(not || quoted) {
			words = append(words, word)
			continue
		}
		flush()
		terms = append(terms, queryTerm{value: value, not: not, quoted: quoted})
	}
	flush()
	return terms
}

// Criteria is the search the server is asked. A bare term searches
// From, To, Cc and Subject, and everything - TEXT, the whole message -
// only on a server that says it holds an index: Dovecot advertises
// SEARCH=FUZZY (RFC 6203) exactly when its full-text plugin is loaded,
// and without an index TEXT over a large folder is a scan of minutes.
// text: asks for the whole message regardless.
//
// Never nil: a query that narrows by nothing is empty criteria, and the
// search path takes a pointer.
func (q Query) Criteria(indexed bool, now time.Time) *imap.SearchCriteria {
	var criteria *imap.SearchCriteria
	for _, term := range q.terms {
		var one *imap.SearchCriteria
		switch {
		case term.key == queryScope:
			continue
		case term.key != "":
			one = operators[term.key](term.value, now)
		case indexed:
			one = &imap.SearchCriteria{Text: []string{term.value}}
		default:
			one = searchCriteriaOr(
				searchCriteriaHeader("From", term.value),
				searchCriteriaHeader("To", term.value),
				searchCriteriaHeader("Cc", term.value),
				searchCriteriaHeader("Subject", term.value),
			)
		}
		if term.not {
			one = &imap.SearchCriteria{Not: []imap.SearchCriteria{*one}}
		}
		criteria = searchCriteriaAnd(criteria, one)
	}
	if criteria == nil {
		criteria = &imap.SearchCriteria{}
	}
	return criteria
}

// WantsText says whether the query asks for the whole message whatever
// the server indexes, which is the search that can take minutes.
func (q Query) WantsText() bool {
	for _, term := range q.terms {
		if term.key == "text" {
			return true
		}
	}
	return false
}

// Scope is the folder in: names, and whether it names all of them.
// Empty and false when the query leaves the scope to the page.
func (q Query) Scope() (folder string, everywhere bool) {
	for _, term := range q.terms {
		if term.key != queryScope || term.not {
			continue
		}
		if value := strings.ToLower(term.value); value == Everywhere || value == "all" {
			return "", true
		}
		folder = term.value
	}
	return folder, false
}

// Excluded are the folders -in: names: where not to look.
func (q Query) Excluded() []string {
	var out []string
	for _, term := range q.terms {
		if term.key == queryScope && term.not {
			out = append(out, term.value)
		}
	}
	return out
}

// addressKeys are the terms that name a person, and so can be checked
// against what the message itself says.
var addressKeys = map[string]bool{"from": true, "to": true, "cc": true, "bcc": true}

// Addressed reports whether the query names an address at all, which is
// what makes checking the answers worth the trouble.
func (q Query) Addressed() bool {
	for _, term := range q.terms {
		if !term.not && addressKeys[term.key] {
			return true
		}
	}
	return false
}

// Addresses are the terms naming a person, by key.
func (q Query) Addresses() map[string][]string {
	out := map[string][]string{}
	for _, term := range q.terms {
		if !term.not && addressKeys[term.key] {
			out[term.key] = append(out[term.key], term.value)
		}
	}
	return out
}

// Widened is the query with every bare term asked of the whole message:
// the link a results page offers when it searched headers. Empty when
// there is no bare term to widen.
func (q Query) Widened() string {
	widened := false
	out := make([]string, 0, len(q.terms))
	for _, term := range q.terms {
		if term.key == "" {
			term.key, widened = "text", true
		}
		out = append(out, term.String())
	}
	if !widened {
		return ""
	}
	return strings.Join(out, " ")
}

// Without is the query less the terms of one key, for a link that
// changes that part of it: in: when a search moves to every folder.
func (q Query) Without(key string) string {
	out := make([]string, 0, len(q.terms))
	for _, term := range q.terms {
		if term.key != key {
			out = append(out, term.String())
		}
	}
	return strings.Join(out, " ")
}

// String writes a term the way it is read back: a value with a space in
// it is one phrase, and quoted it stays one.
func (t queryTerm) String() string {
	value := t.value
	if t.quoted || (t.key != "" && strings.ContainsRune(value, ' ')) {
		value = `"` + value + `"`
	}
	if t.key != "" {
		value = t.key + ":" + value
	}
	if t.not {
		return "-" + value
	}
	return value
}
