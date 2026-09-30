package mail

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/search"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Act is a change made to rows, account by account and folder by folder.
type Act struct {
	announce func(key Ref, uids []imap.UID)
	do       func(c *imapclient.Client, key Ref, uids []imap.UID) error
}

// Failure is an account that did not take an act; Err is nil where it is not signed in.
type Failure struct {
	Account string
	Err     error
}

// ActOn makes the act on each account's rows among logins, the accounts signed in. Where no server
// answered, the first failure stands for all as err.
func ActOn(logins []*alborz.Login, refs []Ref, act Act) (done int, failed []Failure, err error) {
	for key, uids := range grouped(refs) {
		i := slices.IndexFunc(logins, func(s *alborz.Login) bool { return s.Username() == key.Account })
		if i < 0 {
			failed = append(failed, Failure{Account: key.Account})
			continue
		}
		if act.announce != nil {
			act.announce(key, uids)
		}
		err := logins[i].DoIMAP(func(c *imapclient.Client) error { return act.do(c, key, uids) })
		if err != nil {
			failed = append(failed, Failure{key.Account, err})
			continue
		}
		done += len(uids)
	}
	down := 0
	for _, f := range failed {
		var upstream alborz.UpstreamError
		if errors.As(f.Err, &upstream) {
			down++
		}
	}
	if down > 0 && down == len(grouped(refs)) {
		return done, failed, failed[0].Err
	}
	return done, failed, nil
}

func (p *Mailboxes) leaving(key Ref, uids []imap.UID) {
	p.messagesLeaving(key.Account, key.Mailbox, uids)
}

// Move moves rows to a folder; landed takes what the server says it put where (COPYUID, RFC 4315), for an undo.
func (p *Mailboxes) Move(folder string, landed *[]Ref) Act {
	return p.moveAct(func(*imapclient.Client, Ref) (string, error) { return folder, nil }, landed)
}

// MoveToRole moves rows to each account's folder of a merged role, as Move does.
func (p *Mailboxes) MoveToRole(role string, landed *[]Ref) Act {
	return p.moveAct(func(c *imapclient.Client, key Ref) (string, error) {
		return p.resolveRole(c, key.Account, role)
	}, landed)
}

func (p *Mailboxes) moveAct(to func(*imapclient.Client, Ref) (string, error), landed *[]Ref) Act {
	return Act{
		announce: p.leaving,
		do: func(c *imapclient.Client, key Ref, uids []imap.UID) error {
			dest := key.Origin
			if dest == "" {
				var err error
				if dest, err = to(c, key); err != nil {
					return err
				}
			}
			moved, err := p.moveMessages(c, key.Account, key.Mailbox, dest, uids)
			for _, uid := range uidNums(moved) {
				*landed = append(*landed, Ref{Account: key.Account, Mailbox: dest, UID: uid, Origin: key.Mailbox})
			}
			return err
		},
	}
}

// Flag sets, adds or removes flags on rows.
func (p *Mailboxes) Flag(op imap.StoreFlagsOp, flags []imap.Flag) Act {
	return Act{
		announce: func(key Ref, uids []imap.UID) { p.flagWriteStarting(key.Account, key.Mailbox, uids, op, flags) },
		do: func(c *imapclient.Client, key Ref, uids []imap.UID) error {
			return p.writeFlags(c, key.Account, key.Mailbox, uids, op, flags)
		},
	}
}

// Colour marks rows with the flag bits of a colour (FlagColorFlags).
func (p *Mailboxes) Colour(add, del []imap.Flag) Act {
	return Act{do: func(c *imapclient.Client, key Ref, uids []imap.UID) error {
		return p.writeColour(c, key.Account, key.Mailbox, uids, add, del)
	}}
}

func (p *Mailboxes) moveMessages(c *imapclient.Client, account, from, to string, uids []imap.UID) (*imapclient.MoveData, error) {
	if err := ensureMailboxSelected(c, from); err != nil {
		return nil, err
	}
	moved, err := c.Move(imap.UIDSetNum(uids...), to).Wait()
	if err != nil {
		return nil, fmt.Errorf("failed to move message: %w", err)
	}
	p.messagesMoved(account, from, to, uids)
	return moved, nil
}

func (p *Mailboxes) writeFlags(c *imapclient.Client, account, mailbox string, uids []imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) error {
	if err := storeFlags(c, mailbox, imap.UIDSetNum(uids...), op, flags); err != nil {
		return err
	}
	p.messagesFlagged(account, mailbox, uids, op, flags)
	return nil
}

func (p *Mailboxes) writeColour(c *imapclient.Client, account, mailbox string, uids []imap.UID, add, del []imap.Flag) error {
	for _, step := range []struct {
		op    imap.StoreFlagsOp
		flags []imap.Flag
	}{{imap.StoreFlagsAdd, add}, {imap.StoreFlagsDel, del}} {
		if len(step.flags) == 0 {
			continue
		}
		if err := p.writeFlags(c, account, mailbox, uids, step.op, step.flags); err != nil {
			return fmt.Errorf("failed to set flag colour: %w", err)
		}
	}
	return nil
}

func uidNums(moved *imapclient.MoveData) []imap.UID {
	if moved == nil {
		return nil
	}
	uids, ok := moved.DestUIDs.(imap.UIDSet)
	if !ok {
		return nil
	}
	nums, _ := uids.Nums()
	return nums
}

// Empty deletes everything in a folder and says how many it deleted.
func (p *Mailboxes) Empty(ctx context.Context, s *alborz.Login, mboxName string) (int, error) {
	var removed int
	err := s.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		var err error
		removed, err = p.empty(c, s.Username(), mboxName)
		return err
	})
	return removed, err
}

func (p *Mailboxes) empty(c *imapclient.Client, account, mboxName string) (int, error) {
	n, err := emptyMailbox(c, mboxName)
	// Expunge can also remove messages already marked deleted elsewhere.
	p.messagesRemoved(account, mboxName)
	return n, err
}

// EmptyRole empties an account's folder of a merged role.
func (p *Mailboxes) EmptyRole(s *alborz.Login, role string) (int, error) {
	var removed int
	err := s.DoIMAP(func(c *imapclient.Client) error {
		folder, err := p.resolveRole(c, s.Username(), role)
		if err != nil {
			return err
		}
		removed, err = p.empty(c, s.Username(), folder)
		return err
	})
	p.viewForgotten(s.Username(), "#"+role)
	return removed, err
}

// The count keeps a caller from saying "deleted" over an already empty folder.
func emptyMailbox(c *imapclient.Client, mboxName string) (int, error) {
	if err := ensureMailboxSelected(c, mboxName); err != nil {
		return 0, err
	}
	n := int(c.Mailbox().NumMessages)
	if n == 0 {
		return 0, nil
	}
	var seq imap.SeqSet
	seq.AddRange(1, 0)
	if err := storeFlags(c, mboxName, seq, imap.StoreFlagsAdd, []imap.Flag{imap.FlagDeleted}); err != nil {
		return 0, fmt.Errorf("failed to add deleted flag: %w", err)
	}
	if err := c.Expunge().Close(); err != nil {
		return 0, fmt.Errorf("failed to expunge mailbox: %w", err)
	}
	return n, nil
}

// A merged view spans accounts, so a selection names each row by account, folder and UID.
type Ref struct {
	Account, Mailbox string
	UID              imap.UID
	// Origin is the folder a moved message came from, carried only by an undo's reference.
	Origin string
}

// String is the form ParseRefs reads: what a row's checkbox carries.
func (r Ref) String() string {
	if r.Origin != "" {
		return fmt.Sprintf("%s|%s|%d|%s", r.Account, r.Mailbox, r.UID, r.Origin)
	}
	return fmt.Sprintf("%s|%s|%d", r.Account, r.Mailbox, r.UID)
}

// ParseRefs reads the rows a form names, each as Ref.String writes it.
func ParseRefs(values []string) ([]Ref, error) {
	var refs []Ref
	for _, v := range values {
		parts := strings.SplitN(v, "|", 4)
		if len(parts) < 3 {
			return nil, fmt.Errorf("not a message reference: %q", v)
		}
		uid, err := ParseUID(parts[2])
		if err != nil {
			return nil, err
		}
		ref := Ref{Account: parts[0], Mailbox: parts[1], UID: uid}
		if len(parts) == 4 {
			ref.Origin = parts[3]
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func grouped(refs []Ref) map[Ref][]imap.UID {
	out := map[Ref][]imap.UID{}
	for _, r := range refs {
		key := Ref{Account: r.Account, Mailbox: r.Mailbox, Origin: r.Origin}
		out[key] = append(out[key], r.UID)
	}
	return out
}

// CreateFolder creates a folder by its full name.
func (p *Mailboxes) CreateFolder(s *alborz.Login, name string) error {
	err := s.DoIMAP(func(c *imapclient.Client) error {
		return c.Create(name, nil).Wait()
	})
	if err == nil {
		p.mailboxCreated(s.Username())
	}
	return err
}

// DeleteFolder deletes a folder and what it holds.
func (p *Mailboxes) DeleteFolder(ctx context.Context, s *alborz.Login, name string) error {
	return s.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		if err := c.Delete(name).Wait(); err != nil {
			return err
		}
		p.mailboxDeleted(s.Username(), name)
		return nil
	})
}

// RenameFolder gives a folder another full name.
func (p *Mailboxes) RenameFolder(ctx context.Context, s *alborz.Login, old, name string) error {
	err := s.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		return c.Rename(old, name, nil).Wait()
	})
	if err == nil {
		p.mailboxDeleted(s.Username(), old)
	}
	return err
}

// Reload drops what is held of a folder's list and the account's counts, so the next page asks the server.
func (p *Mailboxes) Reload(account, folder string) {
	p.viewForgotten(account, folder)
}

// ReloadRole is Reload for an account's part of a merged role.
func (p *Mailboxes) ReloadRole(account, role string) {
	p.viewForgotten(account, "#"+role)
}

// Changed drops what is held of an account's lists, after a change they depend on.
func (p *Mailboxes) Changed(account string) {
	p.accountChanged(account)
}

// Delete deletes rows; left takes how many their folder still holds.
func (p *Mailboxes) Delete(left *int) Act {
	return Act{
		announce: p.leaving,
		do: func(c *imapclient.Client, key Ref, uids []imap.UID) error {
			var err error
			*left, err = p.removeMessages(c, key, uids)
			return err
		},
	}
}

// Changing tells each account its lists changed as the act is made on it, for a view that spans them.
func (p *Mailboxes) Changing(act Act) Act {
	each := act.do
	act.do = func(c *imapclient.Client, key Ref, uids []imap.UID) error {
		defer p.accountChanged(key.Account)
		return each(c, key, uids)
	}
	return act
}

// removeMessages deletes a folder's messages and says how many it still holds.
func (p *Mailboxes) removeMessages(c *imapclient.Client, key Ref, uids []imap.UID) (int, error) {
	defer p.messagesRemoved(key.Account, key.Mailbox)
	if err := deleteMessages(c, key.Mailbox, imap.UIDSetNum(uids...)); err != nil {
		return 0, err
	}
	return int(c.Mailbox().NumMessages), nil
}

func viewUIDs(c *imapclient.Client, settings *Settings, query, view string) ([]imap.UID, error) {
	criteria := listCriteria(c, settings, query, view)
	if criteria == nil {
		criteria = &imap.SearchCriteria{}
	}
	data, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, err
	}
	uids := data.AllUIDs()
	// The list drops what the envelope contradicts (carried), so the selection must too.
	q := search.Parse(query)
	if len(uids) == 0 || !q.Addressed() {
		return uids, nil
	}
	kept, err := carried(c, q, imap.UIDSetNum(uids...))
	if err != nil {
		return nil, err
	}
	return slices.Collect(maps.Keys(kept)), nil
}

// FolderView lists what a folder's view holds, off the connection the reader's clicks wait on.
func FolderView(ctx context.Context, login *alborz.Login, mboxName, query, view string) ([]imap.UID, error) {
	settings, err := LoadSettings(ctx, login.Store())
	if err != nil {
		return nil, err
	}
	var uids []imap.UID
	err = login.DoIMAPWork(ctx, alborz.IMAPScan, alborz.ScanTimeout, func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}
		var err error
		uids, err = viewUIDs(c, settings, query, view)
		return err
	})
	return uids, err
}

// ViewRefs are what a merged view holds on one account: its role's folder, or every folder a search spans.
func (p *Mailboxes) ViewRefs(ctx context.Context, s *alborz.Login, role string, spanning bool, settings *Settings, query, view string) ([]Ref, error) {
	var folders []string
	if spanning {
		sb, err := p.Rail(s)
		if err != nil {
			return nil, err
		}
		folders = searchFolders(sb.Mailboxes, search.Parse(query))
	}
	var refs []Ref
	err := s.DoIMAPWork(ctx, alborz.IMAPScan, alborz.ScanTimeout, func(c *imapclient.Client) error {
		if !spanning {
			folder, err := p.resolveRole(c, s.Username(), role)
			if err != nil || folder == "" {
				return err
			}
			folders = []string{folder}
		}
		for _, folder := range folders {
			if err := ensureMailboxSelected(c, folder); err != nil {
				return err
			}
			uids, err := viewUIDs(c, settings, query, view)
			if err != nil {
				return err
			}
			for _, uid := range uids {
				refs = append(refs, Ref{Account: s.Username(), Mailbox: folder, UID: uid})
			}
		}
		return nil
	})
	return refs, err

}

// FolderRefs names rows of one folder.
func FolderRefs(account, mailbox string, uids []imap.UID) []Ref {
	refs := make([]Ref, len(uids))
	for i, uid := range uids {
		refs[i] = Ref{Account: account, Mailbox: mailbox, UID: uid}
	}
	return refs
}
