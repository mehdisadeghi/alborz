package mail

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

// The authserv-id is observed on delivered mail and offered for
// confirmation: trusting a guessed id would believe only forged lines.
const (
	// Enough for one id to dominate, few enough for one fetch.
	authSampleSize = 25
	// The id changes only when a server is reconfigured.
	authSampleTTL = time.Hour
)

// SuggestAuthServ names the id this account's server appears to use, or "".
func (p *plugin) SuggestAuthServ(ctx *alborz.Context) string {
	login := ctx.Login
	return p.authServGuesses.Warm(login.Username(), func() (string, error) {
		return sampleAuthServ(login)
	})
}

func sampleAuthServ(login *alborz.Login) (string, error) {
	section := &imap.FetchItemBodySection{
		Specifier: imap.PartSpecifierHeader,
		HeaderFields: []string{
			"Authentication-Results",
			"Received",
		},
		Peek: true,
	}

	// Counted twice when the verdict's writer also took delivery.
	counts := make(map[string]int)
	err := login.DoIMAPBackground(func(c *imapclient.Client) error {
		// Read-write: after EXAMINE the next STORE on this connection fails.
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

func receivedBy(value string) string {
	fields := strings.Fields(value)
	for i, field := range fields {
		if strings.EqualFold(field, "by") && i+1 < len(fields) {
			return strings.ToLower(strings.Trim(fields[i+1], ";"))
		}
	}
	return ""
}

// A server may sign as "mail.example.org" and receive as "example.org".
func sameHost(a, b string) bool {
	a, b = strings.TrimSuffix(a, "."), strings.TrimSuffix(b, ".")
	if a == "" || b == "" {
		return false
	}
	return a == b ||
		strings.HasSuffix(a, "."+b) ||
		strings.HasSuffix(b, "."+a)
}

// A close race is no answer; a pool's hosts count as one family.
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

func serverFamily(id string) string {
	_, rest, ok := strings.Cut(id, ".")
	if !ok || !strings.Contains(rest, ".") {
		return id
	}
	return rest
}

// TrustedAuthServ is the id whose verdicts are believed for this account.
func (p *plugin) TrustedAuthServ(ctx *alborz.Context, settings *Settings) string {
	if settings.TrustedAuthServ != "" {
		return settings.TrustedAuthServ
	}
	return p.SuggestAuthServ(ctx)
}
