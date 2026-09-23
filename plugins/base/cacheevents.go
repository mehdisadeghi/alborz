package alborzbase

import (
	"slices"

	"github.com/emersion/go-imap/v2"
)

// Every change the server has confirmed reaches the caches through this
// file. Listings and bodies hold the same facts twice - a flag, a
// message's presence, a mailbox's identity - and a path that corrects
// one and forgets the other leaves a stale star or a message that comes
// back unread, which nothing upstream contradicts. Keeping each pair in
// one place is what stops the pairing from living in the memory of
// whoever writes the next handler; cacheevents_test.go fails when a
// caller goes around it.

// A write the reader asked for supersedes the automatic read the page
// started, so the pending mark is cancelled before the write is queued
// and cannot land after it.
func flagWriteStarting(user, mailbox string, uids []imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) {
	if op == imap.StoreFlagsSet || slices.Contains(flags, imap.FlagSeen) {
		bodies.cancelSeen(user, mailbox, uids)
	}
}

// messagesLeaving is the same cancellation for a move or a delete: the
// message is about to be gone, and marking it read afterwards would
// write to a UID the folder no longer has.
func messagesLeaving(user, mailbox string, uids []imap.UID) {
	bodies.cancelSeen(user, mailbox, uids)
}

// Plain pages take the new flags in place; the listings pass them on to
// the bodies, which render the same star. Called on the connection
// turn that stored them, for the reason messageRead is.
func messagesFlagged(user, mailbox string, uids []imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) {
	listings.flags(user, mailbox, uids, op, flags)
}

// messageRead is the page's own read of a message, confirmed by the
// server. Called on the connection turn that read it, so a later write
// the reader asked for cannot be patched over.
func messageRead(user, mailbox string, uid imap.UID) {
	listings.markSeen(user, mailbox, uid)
	bodies.flags(user, mailbox, []imap.UID{uid}, imap.StoreFlagsAdd, []imap.Flag{imap.FlagSeen})
}

// A moved message is a different message where it lands, with a UID of
// its own, so the body held here is of no use there.
func messagesMoved(user, from, to string, uids []imap.UID) {
	bodies.discard(user, from, uids)
	listings.evict(user, from)
	listings.evict(user, to)
}

// An expunge also removes what was marked deleted elsewhere, so the
// folder's bodies go entirely rather than by name.
func messagesRemoved(user, mailbox string) {
	bodies.discard(user, mailbox, nil)
	listings.evict(user, mailbox)
}

// A deleted folder can be the one a merged view drew from, and its name
// is free to be created again.
func mailboxDeleted(user, mailbox string) {
	bodies.discard(user, mailbox, nil)
	listings.evictAll(user)
}

// A SELECT said which incarnation of the folder this is. One that
// differs from the last takes the bodies and the pending reads with it:
// their UIDs name other messages now.
func mailboxIdentified(user, mailbox string, validity uint32) {
	bodies.observe(user, mailbox, validity)
}

// The watcher heard of a change nobody asked for. The pages stay
// readable and renew behind whoever opens them next.
func mailboxStale(user, mailbox string) {
	listings.stale(user, mailbox)
}

// What the watcher was told about one message's flags, which is less
// than a refetch and enough for the row.
func mailboxFlagsAnnounced(user, mailbox string, seqNum uint32, flags []imap.Flag) {
	listings.setFlags(user, mailbox, seqNum, flags)
}

// A reader who asks for a refresh means the cached view, whatever its
// age says.
func viewForgotten(user, view string) {
	listings.evict(user, view)
}

// Sending, importing, creating a folder, changing settings: each
// changes what any view of the account would show.
func accountChanged(user string) {
	listings.evictAll(user)
}

// forgetAccount drops everything held for an account once it is gone:
// its listings and sidebar, the people it writes to, its folder roles
// and what its server calls itself. A cache with nobody behind it is a
// leak, and until now only the listings were let go.
func forgetAccount(user string) {
	listings.evictAll(user)
	accountSidebars.Forget(user)
	bodies.forget(user)
	senderBooks.Forget(user)
	correspondents.Forget(user)
	unifiedFolders.Forget(user)
	authServGuesses.Forget(user)
}
