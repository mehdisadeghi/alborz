// Package brands is a book of the names scams borrow - shops, banks, carriers, hosting - with the
// domains each really sends from, and a check of a message's author against it (ADR 82). The
// mail package reads the reader's own inbox for borrowed names; this book knows the names an
// inbox never held. brands.toml beside this file is the book, and says how to keep it.
//
// # Reading a name
//
// A name is read as a person reads it. Compatibility forms and accents fold away (NFKD without
// marks), invisible format characters are dropped, case is ignored, and three or more single
// letters in a row are one word ("A W S"). A word holding a Cyrillic or Greek letter that passes
// for a Latin one, a digit among letters (0 for o, 1 for l or i) or a capital I after a small
// letter (PayPaI) is also read as it looks, against names of four letters and more. An l written
// for an i, and rn for m, are not read: honest words are spelled that way too (Royal Mall).
//
// A name in the book counts where it stands as whole words, never as part of one. One the book
// lists as alone, an ordinary word or a surname (Wise, Visa, Chase), and any name spelled with
// spaced letters, counts only where every other word is a service word (Support, Billing, GmbH).
//
// # When the check speaks
//
// The author's display name holds a name in the book and the From address is at none of that
// brand's domains: a caution, and an alarm when the subject also mentions money or access. A
// domain is the brand's when the book lists it or a parent of it, when the registrable name
// holds the name read (amazon.de, paypal-communication.com; a name under four letters must
// begin it or a hyphenated part: dhl-news.com), or when it is under the brand's own top-level
// domain (signin.aws). Labels below the registrable name are the sender's to choose and count
// for nothing, and neither does a domain the book lists as open to anyone (googlemail.com).
//
// Where the display name is service words alone (Billing, Customer Service) and the subject both
// mentions money and names a brand that is not listed alone, the same is said of the subject, as
// a caution. A Reply-To at a third domain is a line of its own beside either.
//
// # When it stays silent
//
// A wrong warning is worse than none. Nothing is said when the address is in the reader's
// address books; when any brand the name holds sends from the domain (Microsoft via LinkedIn,
// from linkedin.com); when the name is the address itself; or of a delivery status report
// (RFC 3464), which the reporting server signs with its product's name.
package brands

import (
	_ "embed"
	"fmt"
	"mime"
	netmail "net/mail"
	"slices"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/text/unicode/norm"

	"git.mehdix.org/alborz/mail"
)

//go:embed brands.toml
var source string

var held = func() *book {
	b, err := read(source)
	if err != nil {
		panic(err)
	}
	return b
}()

// Check is the book's check of a message's author, for mail.AddCheck.
func Check(e *mail.Evidence) []mail.Indicator { return held.check(e) }

// A name of fewer letters is too near other words to be read by its looks.
const lookalikeMin = 4

type book struct {
	// names are sorted longest first, so "Meta Business" is found before "Meta".
	names   []name
	service map[string]bool
	open    []string
}

type name struct {
	said    string
	words   []word
	joined  string
	alone   bool
	domains []string
}

// word is one word of a name: plain is folded, and like, where the word holds a lookalike, is
// the word as it looks.
type word struct {
	plain, like string
	spaced      bool
}

func read(source string) (*book, error) {
	var file struct {
		Service, Open []string
		Brand         []struct{ Names, Alone, Domains []string }
	}
	meta, err := toml.Decode(source, &file)
	if err != nil {
		return nil, err
	}
	if extra := meta.Undecoded(); len(extra) > 0 {
		return nil, fmt.Errorf("brands: unknown key %s", extra[0])
	}
	b := &book{service: map[string]bool{}, open: file.Open}
	for _, s := range file.Service {
		for _, w := range words(s) {
			b.service[w.plain] = true
		}
	}
	seen := map[string]bool{}
	for _, brand := range file.Brand {
		if len(brand.Names)+len(brand.Alone) == 0 || len(brand.Domains) == 0 {
			return nil, fmt.Errorf("brands: a brand needs a name and a domain: %v %v", brand.Names, brand.Domains)
		}
		for i, said := range slices.Concat(brand.Names, brand.Alone) {
			n := name{said: said, words: words(said), alone: i >= len(brand.Names), domains: brand.Domains}
			spelled := ""
			for k := range n.words {
				n.words[k].like = strings.ReplaceAll(n.words[k].plain, "i", "l")
				n.joined += strings.ReplaceAll(n.words[k].plain, "+", "")
				spelled += " " + n.words[k].plain
			}
			if n.joined == "" || seen[spelled] {
				return nil, fmt.Errorf("brands: %q is empty or listed twice", said)
			}
			seen[spelled] = true
			b.names = append(b.names, n)
		}
	}
	slices.SortStableFunc(b.names, func(x, y name) int { return len(y.words) - len(x.words) })
	return b, nil
}

func (b *book) check(e *mail.Evidence) []mail.Indicator {
	at := strings.LastIndex(e.From, "@")
	host := strings.ToLower(strings.TrimSuffix(e.From[at+1:], "."))
	if at < 0 || host == "" || isReport(e) {
		return nil
	}
	var author []word
	if !strings.EqualFold(strings.Trim(e.Name, ` "'<>`), e.From) {
		author = words(e.Name)
	}
	key, grade := "brands.elsewhere", mail.Caution
	found := b.find(author, false)
	switch {
	case len(found) > 0 && e.MoneySubject():
		grade = mail.Alarm
	case len(found) == 0 && len(author) > 0 && b.serviceOnly(author) && e.MoneySubject():
		key, found = "brands.subject", b.find(words(e.Subject), true)
	}
	if len(found) == 0 || slices.ContainsFunc(found, func(n *name) bool { return b.sends(n, host) }) {
		return nil
	}
	if e.Known != nil && e.Known(e.From) {
		return nil
	}
	out := []mail.Indicator{{Key: key, Grade: grade, Args: []any{spelled(found[0].said), mail.LTR(site(host))}}}
	if reply := b.repliesTo(e, found, host); reply != "" {
		out = append(out, mail.Indicator{Key: "brands.replyto", Grade: grade, Args: []any{mail.LTR(reply)}})
	}
	return out
}

// find lists the book's names the words spell, the leftmost first; a subject is read for the
// names that count anywhere.
func (b *book) find(ws []word, subject bool) []*name {
	var out []*name
	for i := range ws {
		for k := range b.names {
			n := &b.names[k]
			if !n.at(ws, i) {
				continue
			}
			end := i + len(n.words)
			if n.alone || slices.ContainsFunc(ws[i:end], func(w word) bool { return w.spaced }) {
				if subject || !b.serviceOnly(ws[:i]) || !b.serviceOnly(ws[end:]) {
					continue
				}
			}
			out = append(out, n)
		}
	}
	return out
}

func (n *name) at(ws []word, i int) bool {
	if i+len(n.words) > len(ws) {
		return false
	}
	byLooks := len([]rune(n.joined)) >= lookalikeMin
	for k, want := range n.words {
		w := ws[i+k]
		if w.plain != want.plain && (!byLooks || w.like != want.like) {
			return false
		}
	}
	return true
}

func (b *book) serviceOnly(ws []word) bool {
	return !slices.ContainsFunc(ws, func(w word) bool { return !b.service[w.plain] })
}

// sends reports whether host is a domain the named brand's mail comes from.
func (b *book) sends(n *name, host string) bool {
	if slices.ContainsFunc(n.domains, func(d string) bool { return within(host, d) }) {
		return true
	}
	if slices.ContainsFunc(b.open, func(d string) bool { return within(host, d) }) {
		return false
	}
	suffix, icann := publicsuffix.PublicSuffix(host)
	if icann && suffix == n.joined {
		return true
	}
	label, _, _ := strings.Cut(site(host), ".")
	if len(n.joined) >= lookalikeMin && strings.Contains(strings.ReplaceAll(label, "-", ""), n.joined) {
		return true
	}
	for part := range strings.SplitSeq(label, "-") {
		if strings.HasPrefix(part, n.joined) {
			return true
		}
	}
	return false
}

// repliesTo is the Reply-To's domain where it is neither the author's nor the brand's.
func (b *book) repliesTo(e *mail.Evidence, found []*name, host string) string {
	list, err := netmail.ParseAddressList(e.Header.Get("Reply-To"))
	if err != nil {
		return ""
	}
	for _, a := range list {
		reply := strings.ToLower(a.Address[strings.LastIndex(a.Address, "@")+1:])
		if site(reply) != site(host) && !slices.ContainsFunc(found, func(n *name) bool { return b.sends(n, reply) }) {
			return site(reply)
		}
	}
	return ""
}

func isReport(e *mail.Evidence) bool {
	kind, params, err := mime.ParseMediaType(e.Header.Get("Content-Type"))
	return err == nil && kind == "multipart/report" && strings.EqualFold(params["report-type"], "delivery-status")
}

func within(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// site is the registrable domain of a host, or the host where it has none.
func site(host string) string {
	if domain, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return domain
	}
	return host
}

// spelled is a name as the page writes it: one in Latin letters stays left to right.
func spelled(s string) any {
	if strings.ContainsFunc(s, func(r rune) bool { return r >= 0x250 }) {
		return s
	}
	return mail.LTR(s)
}

// lookalikes pairs letters with the Latin ones they pass for, as each case looks.
var lookalikes = func() map[rune]rune {
	const pairs = "АaаaВbЕeеeКkМmНhОoоoРpрpСcсcТtУyуyХxхxЅsѕsІiіiЈjјjӀlӏlԁdԚqԛqԜwԝwҺhһh" + // Cyrillic
		"ΑaαaΒbΕeΖzΗhΙiιiΚkκkΜmΝnνvΟoοoΡpρpΤtΥyυuΧxϳj" + // Greek
		"ıiɑaɡgǀløołlđd" // Latin
	m := map[rune]rune{}
	r := []rune(pairs)
	for i := 0; i < len(r); i += 2 {
		m[r[i]] = r[i+1]
	}
	return m
}()

// words splits a text into its words, folded.
func words(s string) []word {
	var raw [][]rune
	var cur []rune
	for _, r := range norm.NFKD.String(s) + " " {
		switch {
		case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Cf, r):
		// A plus ends a name's word: Disney+.
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '+' && len(cur) > 0:
			cur = append(cur, r)
		case len(cur) > 0:
			raw, cur = append(raw, cur), nil
		}
	}
	var out []word
	for i := 0; i < len(raw); i++ {
		run := i
		for run < len(raw) && len(raw[run]) == 1 {
			run++
		}
		if run-i >= 3 {
			out = append(out, fold(slices.Concat(raw[i:run]...), true))
			i = run - 1
			continue
		}
		out = append(out, fold(raw[i], false))
	}
	return out
}

func fold(raw []rune, spaced bool) word {
	var plain, like strings.Builder
	odd := false
	for i, r := range raw {
		plain.WriteRune(unicode.ToLower(r))
		c, ok := lookalikes[r]
		switch {
		case ok:
		case r == '0':
			c, ok = 'o', true
		case r == '1':
			c, ok = 'l', true
		case r == 'I' && i > 0 && unicode.IsLower(raw[i-1]):
			c, ok = 'l', true
		default:
			c = unicode.ToLower(r)
		}
		odd = odd || ok
		// One letter stands for i and l, which a 1 and a capital I pass for alike.
		if c == 'i' {
			c = 'l'
		}
		like.WriteRune(c)
	}
	w := word{plain: plain.String(), spaced: spaced}
	if odd {
		w.like = like.String()
	}
	return w
}
