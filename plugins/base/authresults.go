package alborzbase

import (
	"strings"

	"github.com/emersion/go-message/textproto"
)

// AuthResults is the receiving server's verdict on a sender (RFC 8601).
// Any sender can write this header, so only the instance our own server
// wrote may be believed: see readAuthResults.
type AuthResults struct {
	SPF, DKIM, DMARC string
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
	fields := h.FieldsByKey("Authentication-Results")
	for fields.Next() {
		id, results, ok := parseAuthResults(fields.Value())
		if !ok || id != trusted {
			continue
		}
		return results
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
