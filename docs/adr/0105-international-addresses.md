# ADR 105: International addresses

Status: accepted.

## Context

An address may carry a domain in U-labels (`grå.org`) or a local part in
UTF-8 (`grå@`). A submission or receiving server without IDNA or
SMTPUTF8 refuses such an address, and a header written in UTF-8 is not
valid RFC 5322 unless every hop speaks RFC 6532.

## Decision

1. A domain with a non-ASCII character MUST be written in A-labels in
   the SMTP envelope and in every address header alborz writes (From,
   To, Cc, Reply-To, Sender), in sent mail and drafts alike. The
   conversion uses the IDNA2008 Lookup profile of `golang.org/x/net/idna`
   (RFC 5891 5, UTS #46 mapping), since an address is typed input to be
   looked up, not a name being registered.
2. A domain in ASCII MUST be left as typed: it is already an A-label or
   LDH name, and STD3 rules would refuse names servers accept.
3. A domain that does not convert MUST refuse the form with
   `form.addressinvalid`, naming the address, before any SMTP command or
   APPEND.
4. An addr-spec still in UTF-8 after rule 1 MUST be sent with
   `MAIL FROM … SMTPUTF8` (RFC 6531 3.4), and its header carries it as
   raw UTF-8 (RFC 6532 3.2). A display name MAY stay an RFC 2047
   encoded-word.
5. When the submission server's EHLO does not list SMTPUTF8, the send
   MUST be refused before MAIL with `form.utf8address`, naming the
   address as typed, and the form keeps what was typed (status 422).
6. alborz MUST NOT enable IMAP `UTF8=ACCEPT` (RFC 9755) while go-imap
   reads every mailbox name as modified UTF-7 and writes `&` as `&-`:
   under `UTF8=ACCEPT` a folder named `Q&A` would fail to list.
7. The Sent copy and a draft MUST be appended as written, in an ordinary
   literal; a server that refuses UTF-8 headers there gives the usual
   draft refusal or follow-up warning.

## Consequences

- Recipients' servers without IDNA support receive mail to IDN domains.
- A reopened draft shows its IDN domains in A-labels.
- A UTF-8 local part works only through a submission server with
  SMTPUTF8; the user learns this on send, not while typing.
- Folder names travel as modified UTF-7 until go-imap decodes mailbox
  names by what was enabled.

## References

- RFC 5890, RFC 5891 (IDNA2008), UTS #46
- RFC 6531 (SMTPUTF8), RFC 6532 (internationalized headers)
- RFC 9755 (IMAP support for UTF-8)
- ADR 4, ADR 57
- `mail/idn.go`, `mail/smtp.go` (`WriteMessage`, `sendMessage`,
  `deliverWithPhase`), `mail/send.go` (`SaveDraft`, `Submit`)
- `locales/*.toml` (`form.addressinvalid`, `form.utf8address`)
