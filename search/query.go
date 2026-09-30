package search

import (
	"bufio"
	"bytes"
	"slices"
	"strings"
)

// Query is a search as the reader typed it, parsed once; what its keys mean is the searcher's.
type Query []Term

// Term is a word or phrase, or a key and its value; Not asks for the opposite.
type Term struct {
	Key, Value string
	Not        bool
	// Quoted keeps a bare phrase one term when the query is written back.
	Quoted bool
}

// Parse reads a query; a key with no value is a word.
func Parse(raw string) Query {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Split(splitSearchTokens)
	var q Query
	for scanner.Scan() {
		token := scanner.Text()
		key, value, keyed := strings.Cut(token, ":")
		not := strings.HasPrefix(key, "-")
		key = strings.ToLower(strings.TrimPrefix(key, "-"))
		switch {
		case phrased([]byte(token)) || !keyed:
			q = append(q, bareTerms(token)...)
		case key == "" || value == "":
			q = append(q, Term{Value: token})
		default:
			q = append(q, Term{Key: key, Value: value, Not: not})
		}
	}
	return q
}

func bareTerms(run string) []Term {
	var terms []Term
	var words []string
	flush := func() {
		if len(words) > 0 {
			terms = append(terms, Term{Value: strings.Join(words, " ")})
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
		terms = append(terms, Term{Value: value, Not: not, Quoted: quoted})
	}
	flush()
	return terms
}

// Keys are the keys the query names, each once, in the order written.
func (q Query) Keys() []string {
	var keys []string
	for _, term := range q {
		if term.Key != "" && !slices.Contains(keys, term.Key) {
			keys = append(keys, term.Key)
		}
	}
	return keys
}

// Words are the query's bare words and phrases.
func (q Query) Words() Query {
	var words Query
	for _, term := range q {
		if term.Key == "" {
			words = append(words, term)
		}
	}
	return words
}

// Without is the query less the terms of one key.
func (q Query) Without(key string) string {
	out := make([]string, 0, len(q))
	for _, term := range q {
		if term.Key != key {
			out = append(out, term.String())
		}
	}
	return strings.Join(out, " ")
}

func (t Term) String() string {
	value := t.Value
	if t.Quoted || (t.Key != "" && strings.ContainsRune(value, ' ')) {
		value = `"` + value + `"`
	}
	if t.Key != "" {
		value = t.Key + ":" + value
	}
	if t.Not {
		return "-" + value
	}
	return value
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
