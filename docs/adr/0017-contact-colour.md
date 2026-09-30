# 17. Where a contact's colour lives

Status: accepted.

## Context

Mail rows carry the seven colours Apple's clients write, and those
colours live on the server: three IMAP keywords beside `\Flagged`,
which every client sees and no browser owns. Contacts are to have the
same seven (TODO 141), and the same question has a different answer
available, because a vCard is a document and alborz has a store of its
own.

Two candidates. An extension property on the card travels: it is in the
object the server holds, every client that round-trips a card keeps
properties it does not understand, and a second alborz on another
machine sees it. A per-account map in alborz's own store cannot be
wrong about anything - no server refuses it, no other client mangles it
- and is invisible to everything else, including the reader's phone.

There is also a wrong answer worth naming: CATEGORIES. It is the
reader's own filing (TODO 140), shared with groups, and shown by every
other client as words on the contact. A colour written there would turn
up in Apple Contacts as a category called "red".

## Decision

The colour is a property of the card: `X-ALBORZ-COLOR`, holding one of
the seven names.

- **It matches mail.** A mark the reader sets is on the object, not in
  the browser that set it. A new browser, a second instance and a
  restart all see the same colours, which is what the mail side already
  promises.
- **The loss it risks is trivial and visible.** A client that drops
  unknown properties on save loses the colour of that one card. That is
  a colour, not data; the alternative loses every colour the moment a
  card is moved between books, because the path it was keyed by has
  changed.
- **Writing it is a write the reader asked for.** Setting a colour is a
  PUT of the card with a new REV, the same as any other edit here, and
  it goes through the same refusal path (a server's no is an answer,
  never a 500).
- **Reading it costs nothing.** The listing already fetches whole
  cards, so the colour is in hand wherever a row is drawn.

The name is alborz's own, spelled once in the code, the way
`/private/vendor/alborz` is on the IMAP side.

## Consequences

A contact carries a colour that other clients ignore but keep. The rail
filters by it the way mail's does, the toolbar sets it the way mail's
does, and the row shows the contact's own colour where there is one and
its address book's where there is not.

If a server or a client is ever found that drops the property, the
answer is not a local map: it is to say so on the Servers page, beside
everything else that is claimed and not kept.
