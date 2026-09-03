package alborzbase

import (
	"net/mail"
	"strings"

	"github.com/emersion/go-message/textproto"
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
}

var authMethods = map[string]bool{"spf": true, "dkim": true, "dmarc": true}

// Failed reports a refusal. Only failures are shown: a mark on every
// message teaches people to ignore marks.
func (a AuthResults) Failed() bool {
	return isFailure(a.SPF) || isFailure(a.DKIM) || isFailure(a.DMARC)
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
func readAuthResults(h textproto.Header, trusted string) *AuthResults {
	trusted = strings.TrimSpace(strings.ToLower(trusted))
	if trusted == "" {
		return nil
	}
	for _, key := range []string{"Authentication-Results", "ARC-Authentication-Results"} {
		fields := h.FieldsByKey(key)
		for fields.Next() {
			id, results, ok := parseAuthResults(fields.Value())
			if !ok || id != trusted {
				continue
			}
			return results
		}
	}
	return nil
}

// parseAuthResults reads an authserv-id and its method=result pairs
// (RFC 8601 2.2).
func parseAuthResults(value string) (string, *AuthResults, bool) {
	parts := strings.Split(value, ";")
	if len(parts) == 0 {
		return "", nil, false
	}

	// An ARC set puts its instance number first: "i=3; mail.example.org".
	// The seal is our own server's either way, so the id is what is read
	// and the instance is not.
	if strings.HasPrefix(strings.TrimSpace(strings.ToLower(parts[0])), "i=") && len(parts) > 1 {
		parts = parts[1:]
	}

	// The id may carry a version: "mx.example.org 1".
	id := strings.ToLower(strings.TrimSpace(parts[0]))
	if i := strings.IndexAny(id, " \t"); i >= 0 {
		id = id[:i]
	}
	if id == "" {
		return "", nil, false
	}

	out := &AuthResults{}
	for _, part := range parts[1:] {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// The rest of the clause names what was checked, and for SPF that
		// includes the envelope sender the verdict is about. Read it
		// before the clause is cut down to its verdict.
		if from := propertyValue(part, "smtp.mailfrom"); from != "" {
			out.MailFrom = from
		}
		// The verdict is the first token; the rest names what was checked.
		if i := strings.IndexAny(part, " \t"); i >= 0 {
			part = part[:i]
		}
		method, verdict, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		method = strings.ToLower(strings.TrimSpace(method))
		if !authMethods[method] {
			continue
		}
		verdict = strings.ToLower(strings.TrimSpace(verdict))
		switch method {
		case "spf":
			out.SPF = verdict
		case "dkim":
			out.DKIM = verdict
		case "dmarc":
			out.DMARC = verdict
		}
	}
	return id, out, true
}

// propertyValue reads one ptype.property of a method clause (RFC 8601
// 2.3). The value may be quoted, and the comment before it may contain
// the same address, so the property is found by name rather than by
// looking for something that resembles an address.
func propertyValue(clause, name string) string {
	i := strings.Index(strings.ToLower(clause), name+"=")
	if i < 0 {
		return ""
	}
	v := strings.TrimSpace(clause[i+len(name)+1:])
	if strings.HasPrefix(v, "\"") {
		if end := strings.IndexByte(v[1:], '"'); end >= 0 {
			return v[1 : end+1]
		}
		return ""
	}
	if end := strings.IndexAny(v, " \t;"); end >= 0 {
		v = v[:end]
	}
	return v
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
