# ADR 90: Mail notification actions and image loading

Status: accepted. Amends ADR 44 (CSS image URLs) and ADR 74 (notification content).

## Decision

- Mail notifications open the exact account, mailbox and UID. They include a subject
  capped at 100 characters and a plain-text excerpt capped at 180 characters. HTML
  contributes text only; attachments, encrypted parts and remote resources are not
  opened. Preview reads use IMAP PEEK and leave unread state unchanged.
  The actual FETCH limits the Subject header to 4 KiB and body part to 16 KiB;
  transfer decoding uses BODYSTRUCTURE instead of fetching a complete MIME header.
- The recipient's server session controls disclosure. Locked, idle-locked and
  unrestored sessions receive the account-only notification. An unlocked passkey
  session receives the preview. Previously delivered notifications remain under
  the operating system's notification controls.
- Supported browsers offer Mark read and Delete. Delete moves the message to
  Trash; no Trash means the action fails. Actions use authenticated same-origin
  POST requests, require a custom header, obey the existing passkey middleware,
  and verify UIDVALIDITY on the same IMAP connection as the mutation. Failed or
  intercepted actions open the exact message for recovery; no mutation is replayed
  automatically after authentication.
  Normal clicks and action recovery use an authenticated notification-open route
  that checks UIDVALIDITY before redirecting to mail. Stale links return to the
  folder without marking a reused UID read.
- Inline CID URLs are percent-decoded before matching Content-ID. CSS image URLs
  follow the same consent and proxy path as image elements. CSS escapes, comments
  and URL syntax that cannot be parsed are discarded. Imports and expressions
  remain blocked.
- Legacy HTTP and protocol-relative image references use HTTPS. Outgoing image requests
  retain public-address checks and HTTPS at every redirect. Hosts without HTTPS
  remain unsupported. The proxy accepts generic MIME headers only when sniffing
  identifies an image, checks upstream status, and buffers at most 5 MiB before
  returning a successful cacheable response.
- Service-worker navigation starts the network and offline-cache lookup together.
  A live response, including a locked session's no-store response, never waits for
  cache storage. A failed network request still waits for an eligible saved copy;
  no-store responses purge saved pages and never fall back to their old content.

## Verification

Local IMAP and encrypted push fixtures cover previews, unread preservation,
Origin/header rejection, stale UIDVALIDITY, marking read and moving to Trash.
Worker fixtures cover exact navigation, action requests and recovery. Image tests
cover encoded CIDs, consent, CSS URLs, generic MIME, failed upstream responses and
oversized chunked bodies. The all-route lock test uses independent clients sharing
one session cookie and also probes background, fragment and no-store behavior.

The lock remains a server-side browser-session lock shared by all its tabs. Other
browser sessions and password-authenticated DAV connections are separate.
