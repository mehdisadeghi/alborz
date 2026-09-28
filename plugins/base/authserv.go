package alborzbase

import (
	"bufio"
	"sort"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/textproto"
)

// Which server may be believed about a sender cannot be guessed from the
// servers Alborz was started with. The authserv-id is what the receiving
// MTA calls itself, set in its own configuration; the IMAP host Alborz
// connects to is a different name, and often a different machine.
//
// Guessing is worse than showing nothing. Trusting an id the server does
// use is safe even against a sender who writes the same one, because the
// server's own line is added at delivery and therefore sits above any
// the sender wrote - which is why only the topmost match is read. But
// trusting an id the server does not use means the only line that ever
// matches is the forged one.
//
// So it is observed rather than assumed: the id our own server wrote on
// mail already delivered here, offered for confirmation and never set
// without it.
const (
	// authSampleSize is how many recent messages are read. Enough for
	// one id to be obviously dominant, few enough to be one fetch.
	authSampleSize = 25
	// authSampleTTL keeps the answer: it changes when a server is
	// reconfigured, which is not something to re-read per page load.
	authSampleTTL = time.Hour
)

// SuggestAuthServ names what this account's own server appears to call
// itself, empty when nothing is clear enough to offer.
func (p *plugin) SuggestAuthServ(ctx *alborz.Context) string {
	login := ctx.Login
	return p.authServGuesses.Warm(login.Username(), func() (string, error) {
		return sampleAuthServ(login)
	})
}

func sampleAuthServ(login *alborz.Login) (string, error) {
	// Both headers of the last messages: the verdict, and the hop that
	// wrote it. Peeked, so reading them marks nothing.
	section := &imap.FetchItemBodySection{
		Specifier: imap.PartSpecifierHeader,
		HeaderFields: []string{
			"Authentication-Results",
			"Received",
		},
		Peek: true,
	}

	// A candidate is counted twice when the hop that wrote the verdict is
	// also the hop that took delivery: that is the difference between
	// "seen often" and "written by our own server".
	counts := make(map[string]int)
	err := login.DoIMAPBackground(func(c *imapclient.Client) error {
		// Read-write, though nothing is written: the client does not
		// remember a read-only selection, and the next STORE on this
		// connection would be refused with the folder still "selected".
		mbox, err := c.Select("INBOX", nil).Wait()
		if err != nil {
			return err
		}
		if mbox.NumMessages == 0 {
			return nil
		}
		from := uint32(1)
		if mbox.NumMessages > authSampleSize {
			from = mbox.NumMessages - authSampleSize + 1
		}
		var seqSet imap.SeqSet
		seqSet.AddRange(from, mbox.NumMessages)

		msgs, err := c.Fetch(seqSet, &imap.FetchOptions{
			BodySection: []*imap.FetchItemBodySection{section},
		}).Collect()
		if err != nil {
			return err
		}
		for _, msg := range msgs {
			raw := msg.FindBodySection(section)
			if raw == nil {
				continue
			}
			id, confirmed := topAuthServ(string(raw))
			if id == "" {
				continue
			}
			counts[id]++
			if confirmed {
				counts[id]++
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return dominant(counts), nil
}

// topAuthServ reads the first Authentication-Results id in a header
// block, and says whether the first Received line names the same host as
// the one that took delivery. Header order is newest first, so the first
// of each is the last hop - our own server.
func topAuthServ(header string) (id string, confirmed bool) {
	h, _ := textproto.ReadHeader(bufio.NewReader(strings.NewReader(header)))
	for _, v := range h.Values("Authentication-Results") {
		if id, _, _ = parseAuthResults(v); id != "" {
			break
		}
	}
	var by string
	for _, v := range h.Values("Received") {
		if by = receivedBy(v); by != "" {
			break
		}
	}
	return id, id != "" && sameHost(id, by)
}

// receivedBy pulls the host out of a Received line's "by" clause, which
// names the machine that accepted the message (RFC 5321 4.4).
func receivedBy(value string) string {
	fields := strings.Fields(value)
	for i, field := range fields {
		if strings.EqualFold(field, "by") && i+1 < len(fields) {
			return strings.ToLower(strings.Trim(fields[i+1], ";"))
		}
	}
	return ""
}

// sameHost accepts a name and the same name one label longer or shorter,
// since a server may sign as "mail.example.org" and receive as
// "example.org" or the reverse. Anything else is a different machine.
func sameHost(a, b string) bool {
	a, b = strings.TrimSuffix(a, "."), strings.TrimSuffix(b, ".")
	if a == "" || b == "" {
		return false
	}
	return a == b ||
		strings.HasSuffix(a, "."+b) ||
		strings.HasSuffix(b, "."+a)
}

// dominant returns the id worth offering: the most common, and only when
// it is most of what was seen. A mailbox whose messages disagree has no
// single answer, and offering the winner of a close race would be the
// guess this is here to avoid.
// A pool's hosts count as one: Migadu writes aspmx1, mx12 and mx13,
// and split three ways none would dominate. The most frequent host of
// the dominant family is named, and its siblings are believed too.
func dominant(counts map[string]int) string {
	total := 0
	families := map[string]int{}
	ids := make([]string, 0, len(counts))
	for id, n := range counts {
		total += n
		families[serverFamily(id)] += n
		ids = append(ids, id)
	}
	if total == 0 {
		return ""
	}
	sort.Slice(ids, func(i, j int) bool { return counts[ids[i]] > counts[ids[j]] })
	best := ids[0]
	if families[serverFamily(best)]*2 <= total {
		return ""
	}
	return best
}

// serverFamily is what two hosts of one pool share: the name past the
// first label, or the whole name when there is nothing to drop.
func serverFamily(id string) string {
	_, rest, ok := strings.Cut(id, ".")
	if !ok || !strings.Contains(rest, ".") {
		return id
	}
	return rest
}

// TrustedAuthServ is the id whose verdicts are believed for this
// account: the one the reader confirmed, or else the one observed on
// the account's own deliveries.
func (p *plugin) TrustedAuthServ(ctx *alborz.Context, settings *Settings) string {
	if settings.TrustedAuthServ != "" {
		return settings.TrustedAuthServ
	}
	return p.SuggestAuthServ(ctx)
}
