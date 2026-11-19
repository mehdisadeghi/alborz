package mail

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Junk rescue moves to INBOX the new unread mail in Junk whose one From address is a contact's and
// whose domain the trusted authserv-id vouches for (ADR 106).

// Contacts change by hand, rarely.
const contactAddressesTTL = 10 * time.Minute

// junkRescue is a watcher's place in its account's Junk; a reconnect under the same UIDVALIDITY keeps it.
type junkRescue struct {
	nudge chan struct{}

	mu       sync.Mutex
	mailbox  string
	validity uint32
	// next is the first UID not yet examined; those below were there when the watcher began, or were seen.
	next imap.UID
}

func newJunkRescue() *junkRescue {
	return &junkRescue{nudge: make(chan struct{}, 1)}
}

func (r *junkRescue) observe(mailbox string, validity uint32, next imap.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mailbox == mailbox && r.validity == validity && r.next != 0 {
		return
	}
	r.mailbox, r.validity, r.next = mailbox, validity, next
}

func (r *junkRescue) place() (string, uint32, imap.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mailbox, r.validity, r.next
}

func (r *junkRescue) advance(validity uint32, next imap.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.validity == validity && next > r.next {
		r.next = next
	}
}

// grew nudges the rescue when the folder named is its Junk.
func (r *junkRescue) grew(mailbox string) {
	if folder, _, _ := r.place(); folder != "" && folder == mailbox {
		notify(r.nudge)
	}
}

// observeJunk records Junk's UIDNEXT on the watcher's connection, before it idles.
func (p *Mailboxes) observeJunk(c *imapclient.Client, r *junkRescue) error {
	if p.ContactAddresses == nil {
		return nil
	}
	mbox, err := getMailboxByRole(c, "junk")
	if err != nil || mbox == nil {
		return err
	}
	status, err := c.Status(mbox.Name(), &imap.StatusOptions{UIDNext: true, UIDValidity: true}).Wait()
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", mbox.Name(), err)
	}
	r.observe(mbox.Name(), status.UIDValidity, status.UIDNext)
	return nil
}

// rescueJunk runs each nudge's rescue for as long as the login lives; a failure waits for the next nudge.
func (p *Mailboxes) rescueJunk(s *alborz.Login, log *log.Logger, changes *alborz.Changes, r *junkRescue) {
	for {
		select {
		case <-s.Done():
			return
		case <-r.nudge:
		}
		moved, err := p.rescueOnce(s, changes, r)
		if err != nil {
			log.Printf("junk rescue %s: %v", s.Username(), err)
		} else if moved > 0 {
			log.Printf("junk rescue %s: moved %d to INBOX", s.Username(), moved)
		}
	}
}

type junkCandidate struct {
	uid  imap.UID
	from string
}

func (p *Mailboxes) rescueOnce(s *alborz.Login, changes *alborz.Changes, r *junkRescue) (int, error) {
	mailbox, validity, next := r.place()
	if mailbox == "" || p.ContactAddresses == nil {
		return 0, nil
	}
	settings, err := LoadSettings(context.Background(), s.Store())
	if err != nil {
		return 0, err
	}
	trusted := p.TrustedAuthServ(s, settings)
	off := settings.NoJunkRescue || trusted == ""

	var candidates []junkCandidate
	var uidNext imap.UID
	err = s.DoIMAPBackground(func(c *imapclient.Client) error {
		sel, err := c.Select(mailbox, nil).Wait()
		if err != nil {
			return fmt.Errorf("failed to select %s: %w", mailbox, err)
		}
		if sel.UIDValidity != validity {
			// Other messages under the old UIDs: begin again from what is there now.
			r.observe(mailbox, sel.UIDValidity, sel.UIDNext)
			return nil
		}
		uidNext = sel.UIDNext
		if off || next >= uidNext {
			return nil
		}
		candidates, err = junkCandidates(c, next, uidNext, trusted)
		return err
	})
	if err != nil || uidNext == 0 {
		return 0, err
	}
	if len(candidates) == 0 {
		r.advance(validity, uidNext)
		return 0, nil
	}

	known, err := p.contactAddresses(s)
	if err != nil {
		return 0, fmt.Errorf("failed to read contacts: %w", err)
	}
	var uids []imap.UID
	for _, m := range candidates {
		if contact, ok := known[m.from]; ok && (contact || settings.JunkRescuePrevious) {
			uids = append(uids, m.uid)
		}
	}
	if len(uids) == 0 {
		r.advance(validity, uidNext)
		return 0, nil
	}

	user := s.Username()
	err = s.DoIMAPBackground(func(c *imapclient.Client) error {
		sel, err := c.Select(mailbox, nil).Wait()
		if err != nil {
			return fmt.Errorf("failed to select %s: %w", mailbox, err)
		}
		if sel.UIDValidity != validity {
			return nil
		}
		p.messagesLeaving(user, mailbox, uids)
		if slices.Contains(c.Mailbox().PermanentFlags, imap.FlagWildcard) {
			if err := p.writeFlags(c, user, mailbox, uids, imap.StoreFlagsAdd, notJunkKeywords[:1]); err != nil {
				return err
			}
		}
		_, err = p.moveMessages(c, user, mailbox, "INBOX", uids)
		return err
	})
	if err != nil {
		return 0, err
	}
	r.advance(validity, uidNext)
	changes.Announce(user, mailbox)
	changes.Announce(user, "INBOX")
	return len(uids), nil
}

// junkCandidates are the unread messages from next up to uidNext not marked junk by hand, each with
// its one From address, whose domain the trusted authserv-id vouches for.
func junkCandidates(c *imapclient.Client, next, uidNext imap.UID, trusted string) ([]junkCandidate, error) {
	var span imap.UIDSet
	span.AddRange(next, uidNext-1)
	found, err := c.UIDSearch(&imap.SearchCriteria{
		UID:     []imap.UIDSet{span},
		NotFlag: append([]imap.Flag{imap.FlagSeen}, junkKeywords...),
	}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}
	uids := found.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}
	section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader,
		HeaderFields: []string{"Authentication-Results"}, Peek: true}
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, Envelope: true,
		BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		return nil, fmt.Errorf("failed to read headers: %w", err)
	}
	var out []junkCandidate
	for _, m := range msgs {
		if m.UID < next || m.UID >= uidNext || m.Envelope == nil {
			continue
		}
		h, err := readHeaderBytes(m.FindBodySection(section))
		if err != nil {
			continue
		}
		if from, ok := vouchedSender(m.Envelope.From, readAuthResults(h, trusted)); ok {
			out = append(out, junkCandidate{uid: m.UID, from: from})
		}
	}
	return out, nil
}

// vouchedSender is the one From address, lower case, when the verdict vouches for its domain.
func vouchedSender(from []imap.Address, results *AuthResults) (string, bool) {
	if len(from) != 1 || results == nil {
		return "", false
	}
	addr := strings.ToLower(from[0].Addr())
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 || !results.Vouches(addr[at+1:]) {
		return "", false
	}
	return addr, true
}

// knownTo reports whether an address is in an address book of any of the accounts; a book that
// cannot be read knows nobody.
func (p *Mailboxes) knownTo(logins []*alborz.Login) func(addr string) bool {
	if p.ContactAddresses == nil {
		return nil
	}
	return func(addr string) bool {
		addr = strings.ToLower(strings.TrimSpace(addr))
		for _, s := range logins {
			if known, err := p.contactAddresses(s); err == nil {
				if _, ok := known[addr]; ok {
					return true
				}
			}
		}
		return false
	}
}

// contactAddresses maps the addresses the account's address books hold, lower case, to whether
// a book of the reader's own holds them rather than Previous recipients alone.
func (p *Mailboxes) contactAddresses(s *alborz.Login) (map[string]bool, error) {
	return p.contacts.Get(s.Username(), func() (map[string]bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), alborz.RoundTripTimeout)
		defer cancel()
		contacts, recipients, err := p.ContactAddresses(ctx, s)
		if err != nil {
			return nil, err
		}
		set := make(map[string]bool, len(contacts)+len(recipients))
		for _, a := range recipients {
			set[strings.ToLower(strings.TrimSpace(a))] = false
		}
		for _, a := range contacts {
			set[strings.ToLower(strings.TrimSpace(a))] = true
		}
		return set, nil
	})
}
