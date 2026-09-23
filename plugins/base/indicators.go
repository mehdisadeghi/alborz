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

// An indicator is one fact about a message that bears on whether to believe
// it, stated as the fact and never as a verdict: the scanner's score,
// who vouched for the domain, whether the reader has ever written to
// the sender. Alborz does not say "this is a scam"; it lays the facts
// beside the message and colours the ones that need no argument. The
// facts come from checks, each a function that reads the evidence and
// says what it saw; a new check is one more function in the list.
//
// A reader who marked the message as not junk has overruled the
// colour, so the mark goes and the facts stay.

// Grade is how an indicator is shown: a plain fact, a yellow caution or a red
// alarm. Only what needs no argument earns a colour.
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

// Score is a spam score with the point it files as junk at, which only
// rspamd's own line names; zero when it did not.
type Score struct {
	Value, Limit float64
	Text         string
}

// Text is the indicator in the page's words.
func (t Indicator) Text(g *alborz.GlobalRenderData) string { return g.Tf(t.Key, t.Args...) }

// Evidence is what the checks read. Relation is known only on the
// message page, where the folders were asked; the list has the header.
type Evidence struct {
	Header  textproto.Header
	Trusted string // the authserv-id the reader confirmed
	Auth    *AuthResults
	Subject string
	From    string // the author's address
	// Named is set when the author's name names a domain the reader
	// gets mail from and the author is not at it; see senderBook.named.
	Named *NamedDomain
	// MoneyWords are the page language's words for money and access,
	// from the locale, split on commas.
	MoneyWords []string
}

// NamedDomain is a name borrowed: the word of the author's name, the
// domain in the reader's inbox it is the name of, and where the author
// actually is.
type NamedDomain struct {
	Word, Known, Sender string
}

type check func(e *Evidence) []Indicator

var checks = []check{scannerScore, twoScanners, failedAuthentication, weakAuthentication, namedElsewhere, moneySubject}

// Indicators runs every check over the evidence, alarms first. The
// one combination that earns a colour on facts that are each plain
// alone - a first message, asking about money or access, with no
// domain vouching - colours those facts rather than adding a sentence
// of its own: the facts are the warning, in their own words.
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

// Mark is the colour a message earns from its indicators, none when the
// reader has said the message is not junk.
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

// Warnings are the indicators that earned a colour: what the page
// shows, and only when there is one. The plain facts stay unspoken; a
// message with nothing against it gets no card.
func Warnings(indicators []Indicator) []Indicator {
	var out []Indicator
	for _, t := range indicators {
		if t.Grade > Fact {
			out = append(out, t)
		}
	}
	return out
}

// notJunkKeywords are what clients set when a reader overrules a junk
// verdict; Apple Mail writes the bare one, others the dollar form.
var notJunkKeywords = []imap.Flag{"$NotJunk", "NotJunk"}

func (msg *IMAPMessage) NotJunk() bool {
	for _, k := range notJunkKeywords {
		if msg.HasFlag(k) {
			return true
		}
	}
	return false
}

// Scanner headers cannot be told apart by where they sit: Postfix puts
// a milter's under its Received line, Migadu appends its own at the
// bottom, a sender writes wherever it likes. So nothing here says who
// wrote a verdict. What can be said is that the message carries the
// marks of two scanner families at once, rspamd's and SpamAssassin's,
// of which at most one is the reader's own server's.
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

// spamdResult is rspamd's own line: "default: False [4.46 / 15.00]".
var spamdResult = regexp.MustCompile(`\[\s*(-?[0-9.]+)\s*/\s*([0-9.]+)\s*\]`)

// scannerScore reads a spam score from the headers, rspamd's line first
// since it names its threshold. A header that appears twice with two
// values is not read: which is the server's cannot be known.
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

// single is the header's one value, or nothing when it has several
// that disagree.
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

// failedAuthentication is the trusted server refusing one of the
// checks, which is a caution and belongs in the same card as the rest:
// a red block of its own beside a yellow card says one thing twice,
// and mail from a home server fails these often. The values are the
// fact; what they mean is the reader's to weigh.
func failedAuthentication(e *Evidence) []Indicator {
	r := e.Auth
	if r == nil || !r.Failed() {
		return nil
	}
	return []Indicator{{Key: "indicator.authfailed", Grade: Caution,
		Args: []any{or(r.SPF, "none"), or(r.DKIM, "none"), or(r.DMARC, "none")}}}
}

// or is the value, or what stands for its absence.
func or(value, empty string) string {
	if value == "" {
		return empty
	}
	return value
}

// weakAuthentication says who vouched for the author's domain, from the
// trusted server's verdict. A pass on every method is nothing to say;
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

// unvouchedRequest says whether the combination holds: somebody the
// reader never wrote to asks about money or access, and nothing vouches
// for who they say they are - either no domain signed for them, or one
// did and it is not the one their name claims. Authentication alone
// vouches for a domain, not for the name written in front of it: a
// hijacked domain passes SPF, DKIM and DMARC for whatever it sends.
func unvouchedRequest(e *Evidence) bool {
	return len(moneySubject(e)) > 0 &&
		(len(weakAuthentication(e)) > 0 || len(failedAuthentication(e)) > 0 || len(namedElsewhere(e)) > 0)
}

// namedElsewhere states the three things the reader can check: what the
// name says, where mail under that name has come from, and where this
// one came from.
func namedElsewhere(e *Evidence) []Indicator {
	if e.Named == nil {
		return nil
	}
	return []Indicator{{Key: "indicator.namedelsewhere", Args: []any{e.Named.Word, e.Named.Known, e.Named.Sender}}}
}

// moneySubject notes a subject that asks for money or a password. The
// words are the language's, from the locale, and the note is a fact
// beside the others, not a colour of its own.
func moneySubject(e *Evidence) []Indicator {
	subject := strings.ToLower(e.Subject)
	for _, word := range e.MoneyWords {
		if word = strings.ToLower(strings.TrimSpace(word)); word != "" && strings.Contains(subject, word) {
			return []Indicator{{Key: "indicator.moneysubject"}}
		}
	}
	return nil
}

// A senderBook is what the reader's own folders know about addresses:
// whom they have written to, from the Sent folder, and whom they have
// junked. Read once an hour per account in two envelope fetches, so a
// list of fifty rows asks nothing per row. Bounded to the newest
// messages of each folder: a Sent folder of years is read for its
// recent correspondents, which is what a first-contact question wants.
type senderBook struct {
	// received holds the registrable domains of the inbox's authors, by
	// their first label: "hetzner" for hetzner.com. It is what lets a
	// name be checked against the reader's own mail and no list of
	// brands: a sender calling itself Hetzner from another domain is a
	// fact only for a reader who has mail from hetzner.com.
	received map[string]string
}

// nameWordMin keeps the comparison to words that can name something:
// "de", "hr" and "the" are words of many names and labels of domains.
const nameWordMin = 4

const (
	senderBookTTL  = time.Hour
	senderBookSpan = 2000
)

var senderBooks = alborz.NewMemo[*senderBook](senderBookTTL)

func senderBookFor(s *alborz.Session) *senderBook {
	return senderBooks.Warm(s.Username(), func() (*senderBook, error) {
		book := &senderBook{}
		err := s.DoIMAPBackground(func(c *imapclient.Client) error {
			authors, _, err := addressesIn(c, "inbox", func(env *imap.Envelope) []imap.Address { return env.From })
			book.received = map[string]string{}
			for addr := range authors {
				domain, label := registrable(addr)
				// Two domains can share a label, and the map's order is
				// random: the same one is named every time.
				if held, ok := book.received[label]; len(label) >= nameWordMin && (!ok || domain < held) {
					book.received[label] = domain
				}
			}
			return err
		})
		return book, err
	})
}

// addressesIn collects the addresses pick chooses from the newest
// messages of the role's folder, lower-cased, and names the folder.
func addressesIn(c *imapclient.Client, role string, pick func(*imap.Envelope) []imap.Address) (map[string]int, string, error) {
	out := map[string]int{}
	mbox, err := getMailboxByRole(c, role)
	if err != nil || mbox == nil {
		return out, "", err
	}
	// Read-write for the same reason as the authserv sample: a
	// read-only selection is not remembered and poisons the next STORE.
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

// relationTo is what the book says about one address.
// registrable is the domain an address could have been registered
// under and its first label: mail.hetzner.com is hetzner.com, "hetzner".
// Empty for an address whose domain is a public suffix or no domain.
func registrable(addr string) (domain, label string) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	domain, err := publicsuffix.EffectiveTLDPlusOne(addr[strings.LastIndex(addr, "@")+1:])
	if err != nil {
		return "", ""
	}
	label, _, _ = strings.Cut(domain, ".")
	return domain, label
}

// named finds a word of the author's name that is the label of a domain
// in the inbox, when the author is at another domain. A sender whose own
// domain carries the word is not borrowing it: PayPal writing from
// paypal.de to a reader who knows paypal.com is PayPal, Deutsche Bahn
// from deutschebahn.com to one who knows bahn.de is the Bahn, and Acme
// from acme.zendesk.com is Acme's desk. Anywhere in the host counts: a
// warning that may be wrong is worse than none.
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
