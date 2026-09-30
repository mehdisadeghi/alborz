package mail

import (
	"context"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message/mail"
)

// A link may choose between the reader's addresses, never choose one for them.
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
	if trust.ours(parsed.Address) {
		return want
	}
	if matchIdentity(settings, parsed.Address) != "" {
		return want
	}
	return ""
}

// Addresses are the login's own and its identities', bare.
func Addresses(ctx context.Context, login *alborz.Login) ([]string, error) {
	settings, err := LoadSettings(ctx, login.Store())
	if err != nil {
		return nil, err
	}
	out := []string{login.Username()}
	for _, identity := range settings.Identities {
		out = append(out, bareAddress(identity))
	}
	return out, nil
}

func matchIdentity(settings *Settings, address string) string {
	for _, identity := range settings.Identities {
		if strings.EqualFold(bareAddress(identity), address) {
			return identity
		}
	}
	return ""
}

func bareAddress(s string) string {
	if parsed, err := mail.ParseAddress(s); err == nil {
		return parsed.Address
	}
	return s
}

func parseIdentities(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func splitIdentityChoice(value string) (account, identity string) {
	account, identity, _ = strings.Cut(value, "|")
	return account, identity
}

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

// Empty leaves the compose form on its own default.
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

// A sender can write any header, so an unchecked address is not shown at all.
type deliveryTrust struct {
	account  string
	domains  []string
	authserv string
}

func (p *plugin) newDeliveryTrust(login *alborz.Login, settings *Settings) deliveryTrust {
	return deliveryTrust{
		account:  login.Username(),
		domains:  p.server.Domains(),
		authserv: p.TrustedAuthServ(login, settings),
	}
}

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

// Everything below our server's Received was in the message before we saw it.
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

// A merged view's row may come from another account.
func (t deliveryTrust) owner(msg *IMAPMessage) string {
	if msg.Account != "" {
		return msg.Account
	}
	return t.account
}

func (t deliveryTrust) alias(msg *IMAPMessage) string {
	if got := t.addresses(msg); len(got) > 0 {
		return got[0]
	}
	return ""
}

// A delivery chain records both alias and mailbox, so meeting the account keeps looking.
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

func writeAs(settings *Settings, trust deliveryTrust, msg *IMAPMessage, lists ...[]imap.Address) string {
	if from := identityAddressed(settings, trust.account, lists...); from != "" {
		return from
	}
	trusted := trust.addresses(msg)
	if from := identityDelivered(settings, trust.account, trusted); from != "" {
		return from
	}
	// A list keyed on an unlisted alias answers to nothing else.
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
