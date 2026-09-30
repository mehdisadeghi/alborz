# 15. What it would take for alborz to be the DAV server

Status: proposed. Nothing here is built; the decision is the shape, and
that the shape waits.

## Context

Alborz is a client. It signs in to somebody else's IMAP, ManageSieve,
CalDAV and CardDAV, and it owns nothing but a cache and a visit. The
question asked was whether it could be the server instead - answering
`PROPFIND` and `REPORT` for a phone's calendar rather than asking them
of a provider - and what that would drag in.

Three things stand in the way, and only one of them is protocol.

**Identity.** Alborz has no users. An account is an address plus a
password that some IMAP server accepted, and that is the whole of the
authorisation model. RFC 3744 principals, which every DAV access
control rests on, need a name that exists whether or not anyone is
signed in, and `DAV:current-user-principal` needs a URL that keeps
meaning something between sessions.

**Storage.** A server owns the bytes. Alborz owns none: it has a
memo cache and a bbolt file for what a browser is reading by, both of
which it would throw away without a second thought.

**Scheduling.** A calendar server that says `calendar-auto-schedule`
(RFC 6638) is on the hook for delivering invitations between its own
users and by iMIP to everyone else, keeping the organiser's copy and
the attendees' in step, and answering free/busy. That is the part
readers actually notice, and it is most of the work.

## Decision

If it is ever built, it is built on IMAP, and the principal is the
IMAP account.

- **Authentication** stays what it already is: the address and password
  reach IMAP, and a DAV request that fails there fails here. The DAV
  username may be anything the reader likes as far as the RFCs are
  concerned, so it is the mail address, which is the one name they
  already have.
- **Storage is a mailbox.** Each collection is an IMAP folder typed
  with METADATA, and each object one message holding one iCalendar or
  vCard - which is what Kolab has done since v3
  (`/private/vendor/kolab/folder-type` = `event.default`), and it is
  the right answer for the same reason: the durability, the quota, the
  backups and the sharing already exist and are somebody else's job.
  ETags come from the message's own identity, and `sync-collection`
  (RFC 6578) from `MODSEQ` where the server has `CONDSTORE`.
  Alborz's own bbolt file is not a candidate: a calendar that lives
  in the cache directory is a calendar that a redeploy loses.
- **The protocol half is go-webdav's.** It ships both sides: a
  `caldav.Handler` and `carddav.Handler` over a `Backend` interface,
  the same package the client already uses. What alborz would write is
  the backend, and the backend is the IMAP mapping above.
- **Scheduling last, and stated.** Until inbox delivery, free/busy and
  organiser copies are all real, the server does not advertise
  `calendar-auto-schedule`. A class in the DAV header is a promise, and
  a half-kept one is worse than a missing one - which is exactly what
  the Servers page now shows about everyone else's server.
- **Proved by CalDAVTester**, Apple's suite, against a rig account, in
  the repository's own scripts rather than by hand. The measure of
  "compliant" is a suite's report, not a reading of the RFC.
- **Right to left from the start.** Nothing in the protocol has a
  direction, but everything a server generates that a person reads
  does: the scheduling messages iMIP sends, the display names it
  defaults to, the error text. Those are locale strings on the server
  side too, in the same four files, and dates in them follow the
  reader's calendar. Retrofitting that after the fact is how every
  other server got it wrong.

## Consequences

Not now. The value of alborz today is that it is a good client to
servers people already run, and a server is a different product with a
different duty of care: it cannot lose data, it cannot be down, and it
cannot be upgraded carelessly. The path above exists so that the answer
to "could it?" is a design rather than a shrug, and so that nothing
built in the meantime - the DAV client, the collection model, the
Servers page - makes it harder.

Files over WebDAV are a separate question and are designed on their own
branch: a file store has no scheduling, no principals worth the name,
and a much smaller protocol surface.
