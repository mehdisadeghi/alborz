package mail

import (
	"slices"

	"github.com/emersion/go-imap/v2"
)

// Listings and bodies hold the same facts twice, so every confirmed change
// reaches both through here.

// Cancelled before the write is queued, so the automatic read cannot land after it.
func (p *Mailboxes) flagWriteStarting(user, mailbox string, uids []imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) {
	if op == imap.StoreFlagsSet || slices.Contains(flags, imap.FlagSeen) {
		p.bodies.cancelSeen(user, mailbox, uids)
	}
}

// Marking a leaving message read would write to a UID the folder no longer has.
func (p *Mailboxes) messagesLeaving(user, mailbox string, uids []imap.UID) {
	p.bodies.cancelSeen(user, mailbox, uids)
}

func (p *Mailboxes) messagesFlagged(user, mailbox string, uids []imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) {
	p.listings.flags(user, mailbox, uids, op, flags)
}

// Called on the connection turn that read it, so a later write cannot be patched over.
func (p *Mailboxes) messageRead(user, mailbox string, uid imap.UID) {
	p.listings.markSeen(user, mailbox, uid)
	p.bodies.flags(user, mailbox, []imap.UID{uid}, imap.StoreFlagsAdd, []imap.Flag{imap.FlagSeen})
}

// A moved message has a new UID where it lands, so the held body is no use there.
func (p *Mailboxes) messagesMoved(user, from, to string, uids []imap.UID) {
	p.bodies.discard(user, from, uids)
	p.listings.evict(user, from)
	p.listings.evict(user, to)
}

// An expunge also removes what was marked deleted elsewhere.
func (p *Mailboxes) messagesRemoved(user, mailbox string) {
	p.bodies.discard(user, mailbox, nil)
	p.listings.evict(user, mailbox)
}

// A merged view may have drawn from the deleted folder.
func (p *Mailboxes) mailboxDeleted(user, mailbox string) {
	p.bodies.discard(user, mailbox, nil)
	p.listings.evictAll(user)
	p.unifiedFolders.Forget(user)
}

// A merged view of a folder by name finds a new one.
func (p *Mailboxes) mailboxCreated(user string) {
	p.listings.evictAll(user)
	p.unifiedFolders.Forget(user)
}

// Under a new UIDVALIDITY the held UIDs name other messages.
func (p *Mailboxes) mailboxIdentified(user, mailbox string, validity uint32) {
	p.bodies.observe(user, mailbox, validity)
}

func (p *Mailboxes) mailboxStale(user, mailbox string) {
	p.listings.stale(user, mailbox)
}

// A folder seen moved behind a listing is sorted again for the fresh one.
func (p *Mailboxes) folderMoved(user, mailbox string) {
	p.listings.reorder(user, mailbox)
}

func (p *Mailboxes) mailboxFlagsAnnounced(user, mailbox string, seqNum uint32, flags []imap.Flag) {
	p.listings.setFlags(user, mailbox, seqNum, flags)
}

// Stale is not enough: a background memo would serve this refresh the old counts.
func (p *Mailboxes) viewForgotten(user, view string) {
	p.listings.evict(user, view)
	p.accountSidebars.Forget(user)
}

func (p *Mailboxes) accountChanged(user string) {
	p.listings.evictAll(user)
}

// ForgetAccount drops what is kept of an account that signed out.
func (p *Mailboxes) ForgetAccount(user string) {
	p.listings.evictAll(user)
	p.accountSidebars.Forget(user)
	p.bodies.forget(user)
	p.senderBooks.Forget(user)
	p.unifiedFolders.Forget(user)
	p.authServGuesses.Forget(user)
}
