package mail

import (
	"bufio"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

// Query is a search as the reader typed it, parsed once.
type Query struct {
	terms []queryTerm
}

type queryTerm struct {
	key, value string
	not        bool
	// quoted keeps a bare phrase one term when the query is written back.
	quoted bool
}

const queryScope = "in"

// Everywhere is the in: value that searches every folder.
const Everywhere = "anywhere"

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
	// SINCE includes the day and BEFORE excludes it (RFC 3501 6.4.4).
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

var queryDayLayouts = []string{"2006-01-02", "2006/01/02"}

func queryDay(value string) (time.Time, bool) {
	for _, layout := range queryDayLayouts {
		if day, err := time.Parse(layout, value); err == nil {
			return day, true
		}
	}
	return time.Time{}, false
}

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

// ParseQuery reads a query; an unknown or unreadable term is searched as text, not dropped.
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

// Criteria is the search the server is asked; never nil.
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

// WantsText reports whether the query asks for the whole message.
func (q Query) WantsText() bool {
	for _, term := range q.terms {
		if term.key == "text" {
			return true
		}
	}
	return false
}

// Scope is the folder in: names, and whether it names all of them.
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

// Excluded are the folders -in: names.
func (q Query) Excluded() []string {
	var out []string
	for _, term := range q.terms {
		if term.key == queryScope && term.not {
			out = append(out, term.value)
		}
	}
	return out
}

var addressKeys = map[string]bool{"from": true, "to": true, "cc": true, "bcc": true}

// Addressed reports whether the query names an address.
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

// Widened is the query with every bare term asked of the whole message.
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

// Without is the query less the terms of one key.
func (q Query) Without(key string) string {
	out := make([]string, 0, len(q.terms))
	for _, term := range q.terms {
		if term.key != key {
			out = append(out, term.String())
		}
	}
	return strings.Join(out, " ")
}

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
