package mail

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/search"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type mailAct struct {
	announce func(key rowRef, uids []imap.UID)
	do       func(c *imapclient.Client, key rowRef, uids []imap.UID) error
}

// err is nil where the account is not signed in.
type actFailure struct {
	account string
	err     error
}

// actOn acts on each account's rows among logins, the accounts signed in.
func actOn(logins []*alborz.Login, refs []rowRef, act mailAct) (done int, failed []actFailure) {
	for key, uids := range grouped(refs) {
		i := slices.IndexFunc(logins, func(s *alborz.Login) bool { return s.Username() == key.account })
		if i < 0 {
			failed = append(failed, actFailure{account: key.account})
			continue
		}
		if act.announce != nil {
			act.announce(key, uids)
		}
		err := logins[i].DoIMAP(func(c *imapclient.Client) error { return act.do(c, key, uids) })
		if err != nil {
			failed = append(failed, actFailure{key.account, err})
			continue
		}
		done += len(uids)
	}
	return done, failed
}

func (p *plugin) leaving(key rowRef, uids []imap.UID) {
	p.messagesLeaving(key.account, key.mailbox, uids)
}

// landed takes what the server says it put where (COPYUID, RFC 4315), for an undo.
func (p *plugin) moveAct(to func(*imapclient.Client, rowRef) (string, error), landed *[]rowRef) mailAct {
	return mailAct{
		announce: p.leaving,
		do: func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
			dest := key.origin
			if dest == "" {
				var err error
				if dest, err = to(c, key); err != nil {
					return err
				}
			}
			moved, err := p.moveMessages(c, key.account, key.mailbox, dest, uids)
			for _, uid := range uidNums(moved) {
				*landed = append(*landed, rowRef{account: key.account, mailbox: dest, uid: uid, origin: key.mailbox})
			}
			return err
		},
	}
}

func (p *plugin) flagAct(op imap.StoreFlagsOp, flags []imap.Flag) mailAct {
	return mailAct{
		announce: func(key rowRef, uids []imap.UID) { p.flagWriteStarting(key.account, key.mailbox, uids, op, flags) },
		do: func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
			return p.writeFlags(c, key.account, key.mailbox, uids, op, flags)
		},
	}
}

func (p *plugin) colourAct(add, del []imap.Flag) mailAct {
	return mailAct{do: func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
		return p.writeColour(c, key.account, key.mailbox, uids, add, del)
	}}
}

func (p *plugin) moveMessages(c *imapclient.Client, account, from, to string, uids []imap.UID) (*imapclient.MoveData, error) {
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

func (p *plugin) writeFlags(c *imapclient.Client, account, mailbox string, uids []imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) error {
	if err := storeFlags(c, mailbox, imap.UIDSetNum(uids...), op, flags); err != nil {
		return err
	}
	p.messagesFlagged(account, mailbox, uids, op, flags)
	return nil
}

func (p *plugin) writeColour(c *imapclient.Client, account, mailbox string, uids []imap.UID, add, del []imap.Flag) error {
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

// empty deletes everything in a folder and says how many it deleted.
func (p *plugin) empty(c *imapclient.Client, account, mboxName string) (int, error) {
	n, err := emptyMailbox(c, mboxName)
	// Expunge can also remove messages already marked deleted elsewhere.
	p.messagesRemoved(account, mboxName)
	return n, err
}

// emptyRole empties an account's folder of a merged role.
func (p *plugin) emptyRole(s *alborz.Login, role string) (int, error) {
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
type rowRef struct {
	account, mailbox string
	uid              imap.UID
	// origin is the folder a moved message came from, carried only by an undo's reference.
	origin string
}

// String is the form parseRefs reads: what a row's checkbox carries.
func (r rowRef) String() string {
	if r.origin != "" {
		return fmt.Sprintf("%s|%s|%d|%s", r.account, r.mailbox, r.uid, r.origin)
	}
	return fmt.Sprintf("%s|%s|%d", r.account, r.mailbox, r.uid)
}

func parseRefs(values []string) ([]rowRef, error) {
	var refs []rowRef
	for _, v := range values {
		parts := strings.SplitN(v, "|", 4)
		if len(parts) < 3 {
			return nil, fmt.Errorf("not a message reference: %q", v)
		}
		uid, err := ParseUID(parts[2])
		if err != nil {
			return nil, err
		}
		ref := rowRef{account: parts[0], mailbox: parts[1], uid: uid}
		if len(parts) == 4 {
			ref.origin = parts[3]
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func grouped(refs []rowRef) map[rowRef][]imap.UID {
	out := map[rowRef][]imap.UID{}
	for _, r := range refs {
		key := rowRef{account: r.account, mailbox: r.mailbox, origin: r.origin}
		out[key] = append(out[key], r.uid)
	}
	return out
}

func createFolder(s *alborz.Login, name string) error {
	return s.DoIMAP(func(c *imapclient.Client) error {
		return c.Create(name, nil).Wait()
	})
}

func (p *plugin) deleteFolder(c *imapclient.Client, account, name string) error {
	if err := c.Delete(name).Wait(); err != nil {
		return err
	}
	p.mailboxDeleted(account, name)
	return nil
}

func renameFolder(c *imapclient.Client, old, name string) error {
	return c.Rename(old, name, nil).Wait()
}

// removeMessages deletes a folder's messages and says how many it still holds.
func (p *plugin) removeMessages(c *imapclient.Client, key rowRef, uids []imap.UID) (int, error) {
	defer p.messagesRemoved(key.account, key.mailbox)
	if err := deleteMessages(c, key.mailbox, imap.UIDSetNum(uids...)); err != nil {
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

// folderViewUIDs lists what a folder's view holds, off the connection the reader's clicks wait on.
func folderViewUIDs(ctx context.Context, login *alborz.Login, mboxName, query, view string) ([]imap.UID, error) {
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

// viewRefs are what a merged view holds on one account: its role's folder, or every folder a search spans.
func (p *plugin) viewRefs(ctx context.Context, s *alborz.Login, role string, spanning bool, settings *Settings, query, view string) ([]rowRef, error) {
	var folders []string
	if spanning {
		sb, err := p.sidebarFor(s)
		if err != nil {
			return nil, err
		}
		folders = searchFolders(sb.mailboxes, search.Parse(query))
	}
	var refs []rowRef
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
				refs = append(refs, rowRef{account: s.Username(), mailbox: folder, uid: uid})
			}
		}
		return nil
	})
	return refs, err

}

func folderRefs(account, mailbox string, uids []imap.UID) []rowRef {
	refs := make([]rowRef, len(uids))
	for i, uid := range uids {
		refs[i] = rowRef{account: account, mailbox: mailbox, uid: uid}
	}
	return refs
}
