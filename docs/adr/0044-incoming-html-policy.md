# ADR 44: Incoming HTML policy

Status: accepted, amended by ADR 90 (percent-decoded `cid:` URLs, CSS
`url()` proxy path and HTTPS upgrade).

## Context

Incoming HTML mail uses presentational markup (`<font>`, `center`, table
attributes) that website comment policies strip, while untrusted mail
must never execute scripts, submit forms or load external resources
without reader consent.

## Decision

1. The incoming sanitizer policy (`incomingPolicy`) MUST be built from
   `bluemonday.NewPolicy()` and explicitly list every allowed element
   and attribute in this repository.
2. Presentational HTML MUST be allowed: `center`, `font` (`color`,
   `size`, `face`), `big`, `small`, and table/cell attributes (`align`,
   `valign`, `bgcolor`, `background`, `width`, `height`, `cellpadding`,
   `cellspacing`, `border`, `nowrap`, `colspan`, `rowspan`).
3. The policy MUST refuse:
   - scripts (`script`, `on*` attributes, `javascript:`/`vbscript:`/`data:`
     URLs, `srcdoc`);
   - self-loading elements (`iframe`, `object`, `embed`, `applet`,
     `link`, `meta`);
   - forms (`form`, `input`, `button`, `textarea`);
   - `base`, `data-*` attributes and HTML comments.
4. Any new element or attribute is allowed only if it cannot execute,
   fetch or address anything outside the message. Message HTML renders
   inside a sandboxed `<iframe srcdoc>` without `allow-scripts`.
5. Inline CSS (`allowedStyles`) blocks `@import` and `expression()`;
   CSS `url()` values follow the image resolution rules (ADR 90).
6. Links and image URLs:
   - allowed link schemes are `http`, `https` and `mailto` only
     (`RequireParseableURLs`);
   - `href` (`a`, `area`) and `cite` MUST be absolute URLs so relative or
     `#fragment` links do not resolve against alborz inside `srcdoc`;
   - `img src`, table/cell `background` and CSS `url()` resolve only to
     a `cid:` or `part:` of the same message, or to `/proxy` for `https`
     remote images when the reader requests remote content (ADR 90);
   - external links receive `target="_blank"` and
     `rel="nofollow noreferrer noopener"`.

## Consequences

- Incoming HTML is enforced by an explicit allowlist and rendered in a
  scriptless sandboxed iframe.
- `#fragment` anchors inside a message are stripped because `srcdoc`
  has no stable base URL.

## References

- ADR 39, ADR 90
- `mail/web/sanitize.go` (`incomingPolicy`, `sanitizeHTML`,
  `allowedStyles`), `mail/web/viewhtml.go`, `mail/richtext.go`
  (`ComposedPolicy`), `mail/web/message_test.go`
