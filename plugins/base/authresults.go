package alborzbase

import (
	"net/mail"
	"strings"

	"github.com/emersion/go-message/textproto"
	"github.com/emersion/go-msgauth/authres"
)

// AuthResults is the receiving server's verdict on a sender (RFC 8601).
// Any sender can write this header, so only the instance our own server
// wrote may be believed: see readAuthResults.
type AuthResults struct {
	SPF, DKIM, DMARC string
	// MailFrom is the envelope sender the SPF check was made about.
	// It is the one address in a message an attacker cannot choose
	// freely: SPF passing means the sending host is authorised for that
	// domain, and our own server is what wrote the verdict down.
	MailFrom string
	// DKIMDomain is the domain whose signature the DKIM verdict is
	// about (header.d), which need not be the author's.
	DKIMDomain string
}

// Failed reports a refusal. Only failures are shown: a mark on every
// message teaches people to ignore marks.
func (a AuthResults) Failed() bool {
	return isFailure(a.SPF) || isFailure(a.DKIM) || isFailure(a.DMARC)
}

// Vouches says the author's domain stood behind this delivery: DMARC
// passed, which is alignment by definition, or DKIM passed for the
// domain itself or a parent of it (RFC 7489 3.1, relaxed).
func (a AuthResults) Vouches(domain string) bool {
	if a.DMARC == "pass" {
		return true
	}
	if a.DKIM != "pass" || domain == "" {
		return false
	}
	d := strings.ToLower(a.DKIMDomain)
	domain = strings.ToLower(domain)
	return d == domain || strings.HasSuffix(domain, "."+d) || strings.HasSuffix(d, "."+domain)
}

// isFailure excludes "none", which means the domain published no policy
// rather than that this message failed one.
func isFailure(verdict string) bool {
	switch verdict {
	case "fail", "softfail", "permerror", "temperror", "policy":
		return true
	}
	return false
}

// readAuthResults returns the verdict written by the trusted server.
// Headers list newest first, so the first match is the one our own
// server added at delivery; anything below it may be the sender's.
//
// A provider's MX pool writes one id per host - Migadu signs as
// aspmx1, mx12 and mx13 under migadu.com - so a sibling host of the
// trusted one counts as it. The topmost-line rule keeps that safe: a
// sender's forged sibling would sit below the server's own line.
func readAuthResults(h textproto.Header, trusted string) *AuthResults {
	trusted = strings.TrimSpace(strings.ToLower(trusted))
	if trusted == "" {
		return nil
	}
	for _, key := range []string{"Authentication-Results", "ARC-Authentication-Results"} {
		fields := h.FieldsByKey(key)
		for fields.Next() {
			id, results, ok := parseAuthResults(fields.Value())
			if !ok || !sameServerFamily(id, trusted) {
				continue
			}
			return results
		}
	}
	return nil
}

// sameServerFamily says whether two authserv-ids are one host or two
// hosts of one pool: the same name, or the same name past the first
// label when both have one to drop.
func sameServerFamily(id, trusted string) bool {
	return id == trusted || serverFamily(id) == serverFamily(trusted)
}

// parseAuthResults reads an authserv-id and its verdicts (RFC 8601 2.2).
func parseAuthResults(value string) (string, *AuthResults, bool) {
	// An ARC set puts its instance number first: "i=3; mail.example.org".
	// The seal is our own server's either way, so the id is what is read
	// and the instance is not.
	if first, rest, ok := strings.Cut(value, ";"); ok && strings.HasPrefix(strings.ToLower(strings.TrimSpace(first)), "i=") {
		value = rest
	}
	id, results, err := authres.Parse(value)
	if err != nil || id == "" {
		return "", nil, false
	}
	out := &AuthResults{}
	for _, result := range results {
		switch r := result.(type) {
		case *authres.SPFResult:
			out.SPF, out.MailFrom = string(r.Value), r.From
		case *authres.DKIMResult:
			out.DKIM, out.DKIMDomain = string(r.Value), r.Domain
		case *authres.DMARCResult:
			out.DMARC = string(r.Value)
		}
	}
	return strings.ToLower(id), out, true
}

// ForwardedBy names the mailbox that passed a message on, or "" when
// nothing can be said for certain.
//
// The evidence a forwarder writes about itself - X-Forwarded-For and
// the like - is written by somebody else's server and cannot be
// checked. This does not use it. What it uses is the envelope sender
// our own server ran SPF against and passed: SPF passing means the
// host that handed us the message is authorised to send for that
// domain, so the address is not one an attacker chose.
//
// A forward shows up as that address belonging to a different domain
// than the message claims to be from: mail from info.ing.de arriving
// with an envelope sender at gmail.com went through a gmail mailbox.
// Ordinary mail is aligned and says nothing; list mail is excluded
// because a list is a relay too and already has its own row.
func ForwardedBy(h textproto.Header, trusted, listID string) string {
	if listID != "" {
		return ""
	}
	results := readAuthResults(h, trusted)
	if results == nil || results.SPF != "pass" || results.MailFrom == "" {
		return ""
	}

	envelope := strings.Trim(strings.TrimSpace(results.MailFrom), "<>")
	at := strings.LastIndex(envelope, "@")
	if at <= 0 {
		return ""
	}
	domain := envelope[at+1:]

	from := h.Get("From")
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return ""
	}
	fromAt := strings.LastIndex(addr.Address, "@")
	if fromAt < 0 || strings.EqualFold(addr.Address[fromAt+1:], domain) {
		return ""
	}

	// A forwarder encodes where it sent the message in a subaddress -
	// Gmail's "+caf_=" among others (RFC 5233). The mailbox is what is
	// worth naming; the routing detail is noise.
	local := envelope[:at]
	if plus := strings.IndexByte(local, '+'); plus > 0 {
		local = local[:plus]
	}
	return local + "@" + domain
}
