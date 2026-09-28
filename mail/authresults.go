package mail

import (
	"net/mail"
	"strings"

	"github.com/emersion/go-message/textproto"
	"github.com/emersion/go-msgauth/authres"
)

// AuthResults is the receiving server's verdict on a sender (RFC 8601).
type AuthResults struct {
	SPF, DKIM, DMARC string
	// MailFrom is the envelope sender the SPF check was made about.
	MailFrom string
	// DKIMDomain is the domain the DKIM verdict is about (header.d).
	DKIMDomain string
}

// Failed reports whether any check failed.
func (a AuthResults) Failed() bool {
	return isFailure(a.SPF) || isFailure(a.DKIM) || isFailure(a.DMARC)
}

// Vouches reports whether domain stood behind this delivery.
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

func isFailure(verdict string) bool {
	switch verdict {
	case "fail", "softfail", "permerror", "temperror", "policy":
		return true
	}
	return false
}

// Only the topmost match is believed: anything below it may be the sender's.
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

func sameServerFamily(id, trusted string) bool {
	return id == trusted || serverFamily(id) == serverFamily(trusted)
}

func parseAuthResults(value string) (string, *AuthResults, bool) {
	// An ARC set puts its instance number first: "i=3; mail.example.org".
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

	// The subaddress is routing detail; the mailbox is what is named.
	local := envelope[:at]
	if plus := strings.IndexByte(local, '+'); plus > 0 {
		local = local[:plus]
	}
	return local + "@" + domain
}
