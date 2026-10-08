package imapsearch

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz/search"
	"github.com/emersion/go-imap/v2"
)

// In is the key that names the folders a search covers.
const In = "in"

// Is is the key that names a message's state.
const Is = "is"

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
	Is: func(value string, _ time.Time) *imap.SearchCriteria {
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
	"after":      parsedDate(queryDayAt, func(day time.Time) *imap.SearchCriteria { return &imap.SearchCriteria{Since: day} }),
	"before":     parsedDate(queryDayAt, func(day time.Time) *imap.SearchCriteria { return &imap.SearchCriteria{Before: day} }),
	"newer_than": parsedDate(queryAgo, func(day time.Time) *imap.SearchCriteria { return &imap.SearchCriteria{Since: day} }),
	"older_than": parsedDate(queryAgo, func(day time.Time) *imap.SearchCriteria { return &imap.SearchCriteria{Before: day} }),
	"larger":     parsedSize(func(size int64) *imap.SearchCriteria { return &imap.SearchCriteria{Larger: size} }),
	"smaller":    parsedSize(func(size int64) *imap.SearchCriteria { return &imap.SearchCriteria{Smaller: size} }),
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

func parsedDate(parse func(string, time.Time) (time.Time, bool), makeCriteria func(time.Time) *imap.SearchCriteria) operator {
	return func(value string, now time.Time) *imap.SearchCriteria {
		day, ok := parse(value, now)
		if !ok {
			return nil
		}
		return makeCriteria(day)
	}
}

func queryDayAt(value string, _ time.Time) (time.Time, bool) { return queryDay(value) }

func parsedSize(makeCriteria func(int64) *imap.SearchCriteria) operator {
	return func(value string, _ time.Time) *imap.SearchCriteria {
		size, ok := querySize(value)
		if !ok {
			return nil
		}
		return makeCriteria(size)
	}
}

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

// Of is the search the server is asked; never nil.
func Of(q search.Query, indexed bool, now time.Time) *imap.SearchCriteria {
	var criteria *imap.SearchCriteria
	for _, term := range resolved(q) {
		var one *imap.SearchCriteria
		switch {
		case term.Key == In:
			continue
		case term.Key != "":
			one = operators[term.Key](term.Value, now)
		case indexed:
			one = &imap.SearchCriteria{Text: []string{term.Value}}
		default:
			one = searchCriteriaOr(
				searchCriteriaHeader("From", term.Value),
				searchCriteriaHeader("To", term.Value),
				searchCriteriaHeader("Cc", term.Value),
				searchCriteriaHeader("Subject", term.Value),
			)
		}
		if term.Not {
			one = &imap.SearchCriteria{Not: []imap.SearchCriteria{*one}}
		}
		criteria = And(criteria, one)
	}
	if criteria == nil {
		criteria = &imap.SearchCriteria{}
	}
	return criteria
}

// resolved is the query with a key nobody knows, or a value its key cannot read, as a word: dropping it would
// answer with the whole folder, which reads as a result.
func resolved(q search.Query) search.Query {
	out := make(search.Query, len(q))
	for i, term := range q {
		read, known := operators[term.Key]
		if term.Key != "" && term.Key != In && (!known || read(term.Value, time.Time{}) == nil) {
			term = search.Term{Value: term.Key + ":" + term.Value, Not: term.Not}
		}
		out[i] = term
	}
	return out
}

// Keys are the keys a search of mail answers.
func Keys() []string {
	return append(slices.Sorted(maps.Keys(operators)), In)
}

// WantsText reports whether the query asks for the whole message.
func WantsText(q search.Query) bool {
	return slices.ContainsFunc(q, func(term search.Term) bool { return term.Key == "text" })
}

// Scope is the folder in: names, and whether it names all of them.
func Scope(q search.Query) (folder string, everywhere bool) {
	for _, term := range q {
		if term.Key != In || term.Not {
			continue
		}
		if value := strings.ToLower(term.Value); value == Everywhere || value == "all" {
			return "", true
		}
		folder = term.Value
	}
	return folder, false
}

// Excluded are the folders -in: names.
func Excluded(q search.Query) []string {
	var out []string
	for _, term := range q {
		if term.Key == In && term.Not {
			out = append(out, term.Value)
		}
	}
	return out
}

var addressKeys = map[string]bool{"from": true, "to": true, "cc": true, "bcc": true}

// Addressed reports whether the query names an address.
func Addressed(q search.Query) bool {
	return slices.ContainsFunc(q, func(term search.Term) bool { return !term.Not && addressKeys[term.Key] })
}

// Addresses are the terms naming a person, by key.
func Addresses(q search.Query) map[string][]string {
	out := map[string][]string{}
	for _, term := range q {
		if !term.Not && addressKeys[term.Key] {
			out[term.Key] = append(out[term.Key], term.Value)
		}
	}
	return out
}

// Widened is the query with every bare term asked of the whole message.
func Widened(q search.Query) string {
	widened := false
	out := make([]string, 0, len(q))
	for _, term := range resolved(q) {
		if term.Key == "" {
			term.Key, widened = "text", true
		}
		out = append(out, term.String())
	}
	if !widened {
		return ""
	}
	return strings.Join(out, " ")
}
