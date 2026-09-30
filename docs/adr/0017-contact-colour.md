# ADR 17: Contact colour

Status: accepted.

## Context

Messages carry seven colours as IMAP keywords (`$MailFlagBit0-2`
beside `\Flagged`), stored on the server. Contacts get the same seven.
A colour can live on the vCard or in alborz's own store; CATEGORIES is
the reader's own filing and other clients show it as text.

## Decision

1. A contact's colour MUST be stored on the vCard as the extension
   property `X-ALBORZ-COLOR`, whose value is one of the seven colour
   names (`alborz.FlagColors`). Any other value reads as no colour.
2. The colour MUST NOT be stored in CATEGORIES or in alborz's store.
3. Setting or clearing a colour is a PUT of the card with a new REV,
   like any other edit; a server refusal is reported as a refusal
   (ADR 42).
4. The colour is read from the cards the listing already fetches; no
   extra request.

## Consequences

- The colour follows the card to every browser and alborz instance.
- A client that drops unknown properties on save loses that card's
  colour; moving a card between books keeps it.
- The list filters by colour and sets it from the star menu, as mail
  does. The star cell shows the contact's colour; the book cell shows
  the address book's.

## References

- RFC 6350 section 6.7.4 (REV), 6.7.1 (CATEGORIES), 6.10 (extension
  properties); RFC 6352 (CardDAV)
- ADR 42
- `pim/contacts/carddav.go` (`colorField`, `CardColor`, `SetColor`,
  `ChangeCard`), `pim/contacts/web/contact.go` (`color`),
  `pim/web/act.go` (`Star`, `Refused`),
  `pim/contacts/web/contacts.go`, `imap.go` (`FlagColors`)
