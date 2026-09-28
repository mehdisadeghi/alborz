package search

import (
	"bytes"

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

func And(criteria ...*imap.SearchCriteria) *imap.SearchCriteria {
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
