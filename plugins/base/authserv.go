package alborzbase

import (
	"sort"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
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

var authServGuesses = alborz.NewMemo[string](authSampleTTL)

// SuggestAuthServ names what this account's own server appears to call
// itself, empty when nothing is clear enough to offer.
func SuggestAuthServ(ctx *alborz.Context) string {
	found, err := authServGuesses.Get(ctx.Session.Username(), func() (string, error) {
		return sampleAuthServ(ctx)
	})
	if err != nil {
		return ""
	}
	return found
}

func sampleAuthServ(ctx *alborz.Context) (string, error) {
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
	err := ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		mbox, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
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
	var by string
	for _, line := range unfold(header) {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "authentication-results":
			if id == "" {
				id, _, _ = parseAuthResults(value)
			}
		case "received":
			if by == "" {
				by = receivedBy(value)
			}
		}
		if id != "" && by != "" {
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

// unfold joins the continuation lines a header may be wrapped over
// (RFC 5322 2.2.3), so a value split across lines is read whole.
func unfold(header string) []string {
	var lines []string
	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += " " + strings.TrimSpace(line)
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// dominant returns the id worth offering: the most common, and only when
// it is most of what was seen. A mailbox whose messages disagree has no
// single answer, and offering the winner of a close race would be the
// guess this is here to avoid.
func dominant(counts map[string]int) string {
	total := 0
	ids := make([]string, 0, len(counts))
	for id, n := range counts {
		total += n
		ids = append(ids, id)
	}
	if total == 0 {
		return ""
	}
	sort.Slice(ids, func(i, j int) bool { return counts[ids[i]] > counts[ids[j]] })
	best := ids[0]
	if counts[best]*2 <= total {
		return ""
	}
	return best
}
