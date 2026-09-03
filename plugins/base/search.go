package alborzbase

import (
	"bufio"
	"bytes"
	"strings"

	"github.com/emersion/go-imap/v2"
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

// TODO: Document search functionality somewhere
//
// Bare terms search TEXT: all headers and the body, matched by the
// server. No capability reliably tells whether the server holds a
// full-text index, so the server is trusted either way; headersOnly
// is the per-account escape to the four-header search when an
// unindexed scan is too slow. from:, subject:, and friends still
// narrow the field, and body: always searches the body.
func PrepareSearch(terms string, headersOnly bool) *imap.SearchCriteria {
	var criteria *imap.SearchCriteria

	scanner := bufio.NewScanner(strings.NewReader(terms))
	scanner.Split(splitSearchTokens)

	for scanner.Scan() {
		term := scanner.Text()
		if !strings.ContainsRune(term, ':') {
			if headersOnly {
				criteria = searchCriteriaAnd(
					criteria,
					searchCriteriaOr(
						searchCriteriaHeader("From", term),
						searchCriteriaHeader("To", term),
						searchCriteriaHeader("Cc", term),
						searchCriteriaHeader("Subject", term),
					),
				)
			} else {
				criteria = searchCriteriaAnd(
					criteria, &imap.SearchCriteria{Text: []string{term}})
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
			default:
				continue
			}
		}
	}

	return criteria
}
