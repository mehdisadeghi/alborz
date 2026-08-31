package alborzbase

import (
	"strings"

	"github.com/emersion/go-message/textproto"
)

// Authentication-Results (RFC 8601) is the receiving server's verdict on
// whether a message's From can be believed: SPF, DKIM and DMARC as it
// judged them. Alborz does no crypto for this and could not - the
// checks happen at delivery, on a message this program never sees in
// transit.
//
// The header is forgeable, and that is the whole difficulty. Anyone can
// put "dmarc=pass" in a message they send; a scammer certainly will. It
// is not forgeable by the one party that matters, the server that
// received the message, because that server writes its own instance at
// the top and no sender can put a line above it.
//
// So the rule is exact: read only the topmost instance whose authserv-id
// is a host the reader has said to trust, and ignore every other. The id
// cannot be inferred - it is whatever the receiving MTA calls itself -
// which is why it is a setting, and why an unset one shows nothing at
// all rather than a guess.
type AuthResults struct {
	// SPF, DKIM and DMARC are the verdicts as written: "pass", "fail",
	// "none", "softfail", "permerror" and so on. Empty means the server
	// did not report on that method.
	SPF, DKIM, DMARC string
}

// authMethods are the results worth showing. Others exist - iprev,
// auth, dkim-adsp - and say nothing a reader can act on.
var authMethods = map[string]bool{"spf": true, "dkim": true, "dmarc": true}

// Failed reports whether any verdict is a refusal. Only failures are
// shown, for the reason the PGP mark is shown that way: a green tick on
// every message teaches people to ignore green ticks, and the failures
// are the small number that mean something.
func (a AuthResults) Failed() bool {
	return isFailure(a.SPF) || isFailure(a.DKIM) || isFailure(a.DMARC)
}

// isFailure separates a refusal from an absence. "none" means the domain
// published no policy, which is not the sender's message failing a check
// - most mail still has no DMARC record, and calling that a failure
// would mark half a mailbox.
func isFailure(verdict string) bool {
	switch verdict {
	case "fail", "softfail", "permerror", "temperror", "policy":
		return true
	}
	return false
}

// readAuthResults finds the verdict written by a server the reader
// trusts. trusted is the authserv-id from the account's settings; with
// none set nothing is read, because a verdict from an unnamed server is
// a verdict from whoever wrote it last.
//
// Headers are listed outermost first, which is newest first, so the
// first match is the topmost instance - the one our own server wrote.
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

// parseAuthResults reads one header value: an authserv-id, then
// "method=result" pairs with properties this does not need
// (RFC 8601 2.2).
func parseAuthResults(value string) (string, *AuthResults, bool) {
	parts := strings.Split(value, ";")
	if len(parts) == 0 {
		return "", nil, false
	}

	// The id may carry a version number after it: "mx.example.org 1".
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
		// "dkim=pass header.d=example.org" - the verdict is the first
		// token, the rest describes what was checked.
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
