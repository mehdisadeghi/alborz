package alborzbase

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/textproto"
	"golang.org/x/net/publicsuffix"

	"git.mehdix.org/alborz"
)

// Indicators state facts, never a verdict; only what needs no argument earns a colour.

// Grade is how an indicator is shown.
type Grade int

const (
	Fact Grade = iota
	Caution
	Alarm
)

type Indicator struct {
	Key   string // locale key under indicator.
	Args  []any
	Grade Grade
}

// Score is a spam score and its junk threshold, zero when unnamed.
type Score struct {
	Value, Limit float64
	Text         string
}

// Text is the indicator in the page's words.
func (t Indicator) Text(g *alborz.GlobalRenderData) string { return g.Tf(t.Key, t.Args...) }

// Evidence is what the checks read.
type Evidence struct {
	Header  textproto.Header
	Trusted string
	Auth    *AuthResults
	Subject string
	From    string
	// Named is set when the author's name borrows a known domain.
	Named *NamedDomain
	// MoneyWords are the locale's words for money and access.
	MoneyWords []string
}

// NamedDomain is a name borrowed from a domain in the reader's inbox.
type NamedDomain struct {
	Word, Known, Sender string
}

type check func(e *Evidence) []Indicator

var checks = []check{scannerScore, twoScanners, failedAuthentication, weakAuthentication, namedElsewhere, moneySubject}

// Indicators runs every check over the evidence, alarms first.
func Indicators(e *Evidence) []Indicator {
	var out []Indicator
	for _, s := range checks {
		out = append(out, s(e)...)
	}
	if unvouchedRequest(e) {
		for i := range out {
			switch out[i].Key {
			case "indicator.spfonly", "indicator.namedelsewhere", "indicator.moneysubject":
				out[i].Grade = Caution
			}
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Grade > out[j-1].Grade; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Mark is the colour a message earns from its indicators.
func Mark(indicators []Indicator, notJunk bool) Grade {
	if notJunk {
		return Fact
	}
	grade := Fact
	for _, t := range indicators {
		if t.Grade > grade {
			grade = t.Grade
		}
	}
	return grade
}

// Warnings are the indicators that earned a colour.
func Warnings(indicators []Indicator) []Indicator {
	var out []Indicator
	for _, t := range indicators {
		if t.Grade > Fact {
			out = append(out, t)
		}
	}
	return out
}

var notJunkKeywords = []imap.Flag{"$NotJunk", "NotJunk"}

func (msg *IMAPMessage) NotJunk() bool {
	for _, k := range notJunkKeywords {
		if msg.HasFlag(k) {
			return true
		}
	}
	return false
}

// Where a verdict sits does not say who wrote it; two families at once do.
var (
	rspamdHeaders       = []string{"X-Spamd-Result", "X-Migadu-Spam-Score", "X-Rspamd-Score", "X-Spamd-Bar"}
	spamAssassinHeaders = []string{"X-Spam-Status", "X-Spam-Checker-Version", "X-Spam-Report", "X-Spam-Level"}
)

func hasAny(h textproto.Header, keys []string) bool {
	for _, k := range keys {
		if h.Has(k) {
			return true
		}
	}
	return false
}

func twoScanners(e *Evidence) []Indicator {
	if !hasAny(e.Header, rspamdHeaders) || !hasAny(e.Header, spamAssassinHeaders) {
		return nil
	}
	return []Indicator{{Key: "indicator.twoscanners"}}
}

var spamdResult = regexp.MustCompile(`\[\s*(-?[0-9.]+)\s*/\s*([0-9.]+)\s*\]`)

func scannerScore(e *Evidence) []Indicator {
	s, ok := readScore(e.Header)
	if !ok {
		return nil
	}
	grade := Fact
	if s.Limit > 0 && s.Value >= s.Limit {
		grade = Caution
	}
	if s.Limit > 0 {
		return []Indicator{{Key: "indicator.scorelimit", Args: []any{s.Text, strconv.FormatFloat(s.Limit, 'f', -1, 64)}, Grade: grade}}
	}
	return []Indicator{{Key: "indicator.score", Args: []any{s.Text}, Grade: grade}}
}

// Of disagreeing values, which is the server's cannot be known.
func single(h textproto.Header, key string) string {
	var value string
	fields := h.FieldsByKey(key)
	for fields.Next() {
		v := strings.TrimSpace(fields.Value())
		if value != "" && v != value {
			return ""
		}
		value = v
	}
	return value
}

func readScore(h textproto.Header) (Score, bool) {
	if line := single(h, "X-Spamd-Result"); line != "" {
		if m := spamdResult.FindStringSubmatch(line); m != nil {
			value, _ := strconv.ParseFloat(m[1], 64)
			limit, _ := strconv.ParseFloat(m[2], 64)
			return Score{Value: value, Limit: limit, Text: m[1]}, true
		}
	}
	for _, key := range []string{"X-Migadu-Spam-Score", "X-Spam-Score", "X-Rspamd-Score"} {
		if v := single(h, key); v != "" {
			if value, err := strconv.ParseFloat(v, 64); err == nil {
				return Score{Value: value, Text: v}, true
			}
		}
	}
	return Score{}, false
}

func failedAuthentication(e *Evidence) []Indicator {
	r := e.Auth
	if r == nil || !r.Failed() {
		return nil
	}
	return []Indicator{{Key: "indicator.authfailed", Grade: Caution,
		Args: []any{or(r.SPF, "none"), or(r.DKIM, "none"), or(r.DMARC, "none")}}}
}

func or(value, empty string) string {
	if value == "" {
		return empty
	}
	return value
}

// SPF alone speaks for the envelope domain, which the sender chose.
func weakAuthentication(e *Evidence) []Indicator {
	r := e.Auth
	if r == nil {
		return nil
	}
	dkim, dmarc := strings.ToLower(r.DKIM), strings.ToLower(r.DMARC)
	if dkim == "pass" || dmarc == "pass" || r.Failed() {
		// A refusal is said once, by failedAuthentication.
		return nil
	}
	return []Indicator{{Key: "indicator.spfonly", Args: []any{r.SPF, r.DKIM, r.DMARC}}}
}

// Authentication vouches for a domain, not for the name in front of it.
func unvouchedRequest(e *Evidence) bool {
	return len(moneySubject(e)) > 0 &&
		(len(weakAuthentication(e)) > 0 || len(failedAuthentication(e)) > 0 || len(namedElsewhere(e)) > 0)
}

func namedElsewhere(e *Evidence) []Indicator {
	if e.Named == nil {
		return nil
	}
	return []Indicator{{Key: "indicator.namedelsewhere", Args: []any{e.Named.Word, e.Named.Known, e.Named.Sender}}}
}

func moneySubject(e *Evidence) []Indicator {
	subject := strings.ToLower(e.Subject)
	for _, word := range e.MoneyWords {
		if word = strings.ToLower(strings.TrimSpace(word)); word != "" && strings.Contains(subject, word) {
			return []Indicator{{Key: "indicator.moneysubject"}}
		}
	}
	return nil
}

type senderBook struct {
	// Keyed by first label, so names are checked against the reader's own mail, not a brand list.
	received map[string]string
}

// Shorter words like "de" or "the" are parts of too many names and domains.
const nameWordMin = 4

const (
	senderBookTTL  = time.Hour
	senderBookSpan = 2000
)

func (p *plugin) senderBookFor(s *alborz.Login) *senderBook {
	return p.senderBooks.Warm(s.Username(), func() (*senderBook, error) {
		book := &senderBook{}
		err := s.DoIMAPBackground(func(c *imapclient.Client) error {
			authors, _, err := addressesIn(c, "inbox", func(env *imap.Envelope) []imap.Address { return env.From })
			book.received = map[string]string{}
			for addr := range authors {
				domain, label := registrable(addr)
				// Two domains can share a label; map order is random, so pick stably.
				if held, ok := book.received[label]; len(label) >= nameWordMin && (!ok || domain < held) {
					book.received[label] = domain
				}
			}
			return err
		})
		return book, err
	})
}

func addressesIn(c *imapclient.Client, role string, pick func(*imap.Envelope) []imap.Address) (map[string]int, string, error) {
	out := map[string]int{}
	mbox, err := getMailboxByRole(c, role)
	if err != nil || mbox == nil {
		return out, "", err
	}
	// Read-write: after EXAMINE the next STORE on this connection fails.
	sel, err := c.Select(mbox.Name(), nil).Wait()
	if err != nil || sel.NumMessages == 0 {
		return out, mbox.Name(), err
	}
	from := uint32(1)
	if sel.NumMessages > senderBookSpan {
		from = sel.NumMessages - senderBookSpan + 1
	}
	var set imap.SeqSet
	set.AddRange(from, sel.NumMessages)
	msgs, err := c.Fetch(set, &imap.FetchOptions{Envelope: true}).Collect()
	if err != nil {
		return out, mbox.Name(), err
	}
	for _, m := range msgs {
		if m.Envelope == nil {
			continue
		}
		for _, a := range pick(m.Envelope) {
			if addr := strings.ToLower(a.Addr()); addr != "" {
				out[addr]++
			}
		}
	}
	return out, mbox.Name(), nil
}

func registrable(addr string) (domain, label string) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	domain, err := publicsuffix.EffectiveTLDPlusOne(addr[strings.LastIndex(addr, "@")+1:])
	if err != nil {
		return "", ""
	}
	label, _, _ = strings.Cut(domain, ".")
	return domain, label
}

// A host carrying the word anywhere is not borrowing it: a wrong warning is worse than none.
func (b senderBook) named(name, addr string) *NamedDomain {
	sender, _ := registrable(addr)
	if sender == "" {
		return nil
	}
	host := strings.ToLower(addr[strings.LastIndex(addr, "@")+1:])
	words := strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, word := range words {
		label := strings.ToLower(word)
		known, ok := b.received[label]
		if !ok || strings.Contains(host, label) || known == sender {
			continue
		}
		return &NamedDomain{Word: word, Known: known, Sender: sender}
	}
	return nil
}
