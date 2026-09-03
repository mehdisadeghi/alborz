package alborzbase

import (
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message/mail"
)

// ownIdentity returns want when it names an address this account may
// write as, and "" otherwise. A link may choose between the addresses
// the reader already owns; it may not choose an address for them.
func ownIdentity(settings *Settings, trust deliveryTrust, want string) string {
	if want == "" {
		return ""
	}
	parsed, err := mail.ParseAddress(want)
	if err != nil {
		return ""
	}
	if strings.EqualFold(parsed.Address, trust.account) {
		return want
	}
	// An address at a domain this server serves may be the reader's even
	// when they have not written it down; one anywhere else cannot be.
	if trust.ours(parsed.Address) {
		return want
	}
	if matchIdentity(settings, parsed.Address) != "" {
		return want
	}
	return ""
}

// matchIdentity names the identity written as the address, or "" when
// none of the account's identities is.
func matchIdentity(settings *Settings, address string) string {
	for _, identity := range settings.Identities {
		if strings.EqualFold(bareAddress(identity), address) {
			return identity
		}
	}
	return ""
}

// bareAddress is the address without the display name an identity or a
// From may carry; an unparsable string is returned as it is.
func bareAddress(s string) string {
	if parsed, err := mail.ParseAddress(s); err == nil {
		return parsed.Address
	}
	return s
}

// parseIdentities reads the settings textarea: one address per line,
// blank lines ignored.
func parseIdentities(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// splitIdentityChoice reads the From dropdown's value: the account, and
// optionally the identity of that account to send as.
func splitIdentityChoice(value string) (account, identity string) {
	account, identity, _ = strings.Cut(value, "|")
	return account, identity
}

// addressed reports whether the lists a message names carry the address.
func addressed(address string, lists ...[]imap.Address) bool {
	for _, list := range lists {
		for _, a := range list {
			if strings.EqualFold(a.Addr(), address) {
				return true
			}
		}
	}
	return false
}

// identityAddressed names the address a reply should be written from:
// the identity the original was addressed to, if one of them was, and
// otherwise the empty string, which leaves the compose form on its own
// default. Comparison is case-insensitive - a mail addressed to
// Mx@43.yt is addressed to the identity mx@43.yt.
func identityAddressed(settings *Settings, username string, lists ...[]imap.Address) string {
	for _, list := range lists {
		for _, addr := range list {
			if strings.EqualFold(addr.Addr(), username) {
				return ""
			}
			if identity := matchIdentity(settings, addr.Addr()); identity != "" {
				return identity
			}
		}
	}
	return ""
}

// deliveryTrust decides which of the addresses a message's delivery path
// named may be shown or written from. Nothing else may: the headers are
// part of the message, so a sender can write any of them, and an address
// that cannot be checked is not displayed at all rather than displayed
// with a caveat nobody reads.
type deliveryTrust struct {
	// account is the address the message landed in.
	account string
	// domains are the mail domains this server serves. An address at
	// one of them can be the reader's; an address anywhere else cannot,
	// whatever the header claims.
	domains []string
	// authserv is what the reader's own server calls itself, from the
	// same setting Authentication-Results is read under. Empty until
	// they name it, and the check below is skipped until they do.
	authserv string
}

func newDeliveryTrust(ctx *alborz.Context, settings *Settings, account string) deliveryTrust {
	return deliveryTrust{
		account:  account,
		domains:  ctx.Server.Domains(),
		authserv: settings.TrustedAuthServ,
	}
}

// ours reports whether an address could be one the reader receives at.
func (t deliveryTrust) ours(addr string) bool {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(addr[at+1:])
	if account := strings.LastIndex(t.account, "@"); account >= 0 &&
		strings.EqualFold(domain, t.account[account+1:]) {
		return true
	}
	for _, served := range t.domains {
		if strings.EqualFold(domain, served) {
			return true
		}
	}
	return false
}

// addresses returns the delivery addresses worth believing for a
// message: at one of our domains, and - once the reader has named their
// own server - written above the Received that server added, since
// everything below it was in the message before we ever saw it.
func (t deliveryTrust) addresses(msg *IMAPMessage) []string {
	account := t.owner(msg)
	addrs, cut := deliveryAddresses(msg.rootHeader, t.authserv)
	var out []string
	for i, addr := range addrs {
		if i >= cut || !t.ours(addr) || strings.EqualFold(addr, account) {
			continue
		}
		out = append(out, addr)
	}
	return out
}

// owner is the account a message landed in: the request's, unless the
// row came from another account of the merged view.
func (t deliveryTrust) owner(msg *IMAPMessage) string {
	if msg.Account != "" {
		return msg.Account
	}
	return t.account
}

// alias is the one address a message reached that is not the account it
// landed in, or "" when there is nothing worth showing.
func (t deliveryTrust) alias(msg *IMAPMessage) string {
	if got := t.addresses(msg); len(got) > 0 {
		return got[0]
	}
	return ""
}

// identityDelivered names the identity a message was delivered to, from
// what the delivery path wrote down rather than from To and Cc. Mail to
// an alias often names the alias nowhere else: To holds the list or the
// original recipient, and only the delivering server records where it
// actually went.
//
// Unlike identityAddressed this does not stop at the account's own
// address. A delivery chain records both - the alias it was sent to and
// the mailbox it ended in - so meeting the account is a reason to keep
// looking rather than to give up.
func identityDelivered(settings *Settings, username string, delivered []string) string {
	for _, addr := range delivered {
		if strings.EqualFold(addr, username) {
			continue
		}
		if identity := matchIdentity(settings, addr); identity != "" {
			return identity
		}
	}
	return ""
}

// writeAs names the address a message should be answered or unsubscribed
// from: the identity it was addressed to, else the one it was delivered
// to. Empty leaves the compose form on its own default.
func writeAs(settings *Settings, trust deliveryTrust, msg *IMAPMessage, lists ...[]imap.Address) string {
	if from := identityAddressed(settings, trust.account, lists...); from != "" {
		return from
	}
	// Only what the delivery path is trusted for: an address a sender
	// wrote into a header is not one of the reader's identities.
	trusted := trust.addresses(msg)
	if from := identityDelivered(settings, trust.account, trusted); from != "" {
		return from
	}
	// An alias at one of our own domains that nobody has written down as
	// an identity is still where the message went, and a list keyed on
	// it will not answer to anything else. Offered bare, since there is
	// no name to go with it.
	if len(trusted) > 0 {
		return trusted[0]
	}
	return ""
}

func filterOutUsername(username string, addresses []imap.Address) []imap.Address {
	for i, addr := range addresses {
		if addr.Addr() == username {
			return append(addresses[:i], addresses[i+1:]...)
		}
	}
	return addresses
}
