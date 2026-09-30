package mail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/pgp"
	"git.mehdix.org/alborz/web"
	"github.com/ProtonMail/go-crypto/openpgp"
	pgperrors "github.com/ProtonMail/go-crypto/openpgp/errors"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/textproto"
)

// SignatureState is a signature verdict; only good and bad are shown.
type SignatureState string

const (
	SignatureNone SignatureState = "" // nothing signed, or no key to check with
	SignatureGood SignatureState = "good"
	SignatureBad  SignatureState = "bad"
)

// KeySource says where the verifying key came from.
type KeySource string

const (
	KeyFromMessage KeySource = "message" // Autocrypt, or attached
	KeyFromDomain  KeySource = "domain"  // the Web Key Directory
	KeyFromCA      KeySource = "ca"      // an S/MIME certificate's issuer
)

// handleSignature answers the page's request for its verdict, which may
// stream the whole signed part; the reader leaving drops the connection.
func (p *plugin) handleSignature(ctx *web.Context) error {
	mboxName, uid, err := MessageRef(ctx)
	if err != nil {
		return err
	}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	v, err := p.verify(ctx.Request().Context(), ctx.Login, mboxName, uid, p.TrustedAuthServ(ctx.Login, settings))
	// A verdict that could not be drawn says nothing, as one without a key does.
	if err != nil {
		ctx.Logger().Printf("signature %q uid %v: %v", mboxName, uid, err)
	}
	return ctx.Render(http.StatusOK, "message-signature", &VerdictRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx), Signature: v})
}

// verify draws the verdict on one message, streaming its signed part; trusted is the account's authserv-id.
func (p *plugin) verify(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID, trusted string) (Verification, error) {
	var v Verification
	err := login.DoIMAPTransfer(ctx, func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}
		msgs, err := c.Fetch(imap.UIDSetNum(uid), partFetchOptions(nil, true, false)).Collect()
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return alborz.NotFound("notfound.message", fmt.Sprint(uid))
		}
		msg, _, err := messagePart(msgs[0], mboxName, uid, nil, false)
		if err != nil {
			return err
		}
		if v, err = p.verifySignature(ctx, p.server.Outgoing.Remote(wkdTimeout), c, msg); err != nil {
			return err
		}
		v = withDelivery(v, readAuthResults(messageRootHeader(msg), trusted))
		return nil
	})
	return v, err
}

// VerdictRenderData fills the page's place for the verdict.
type VerdictRenderData struct {
	web.BaseRenderData
	Signature Verification
}

// Verification is a message's authenticity verdict, drawn outside the frame the message controls.
type Verification struct {
	State SignatureState
	// Signer is the address the verifying key claims, set only for a good signature.
	Signer string
	// Reason is a locale key naming what went wrong, for a bad signature.
	Reason string
	// Source is where the key that verified came from.
	Source KeySource
	// Claim is a locale key naming what was checked, for a good signature.
	Claim string
	// Confirmed says the domain published the key and our server saw the domain sign the delivery.
	Confirmed bool
}

// The only bad verdict: a missing or foreign key is absence of evidence, not forgery.
const reasonUnverified = "pgp.reasonunverified"

const pgpProtocol = "application/pgp-signature"

// Only the root counts: a signed part nested in unsigned mail says nothing about the message around it.
func isSigned(bs imap.BodyStructure) bool {
	multi, ok := bs.(*imap.BodyStructureMultiPart)
	if !ok || !strings.EqualFold(multi.Subtype, "signed") || len(multi.Children) != 2 {
		return false
	}
	second, ok := multi.Children[1].(*imap.BodyStructureSinglePart)
	if !ok {
		return false
	}
	media := strings.ToLower(second.MediaType())
	return media == pgpProtocol || smimeProtocols[media]
}

// verifySignature draws the verdict on msg from its parts on c; the signed
// part streams through the check, never held, and only when a check can come of it.
func (p *plugin) verifySignature(ctx context.Context, client *http.Client, c *imapclient.Client, msg *IMAPMessage) (Verification, error) {
	if !isSigned(msg.BodyStructure) {
		return Verification{}, nil
	}
	multi := msg.BodyStructure.(*imap.BodyStructureMultiPart)
	second := multi.Children[1].(*imap.BodyStructureSinglePart)
	protocol := strings.ToLower(second.MediaType())
	if multi.Extended == nil || !strings.EqualFold(multi.Extended.Params["protocol"], protocol) || second.Size > partReadMax {
		return Verification{}, nil
	}

	inner := &imap.FetchItemBodySection{Peek: true, Part: []int{1}, Specifier: imap.PartSpecifierMIME}
	sig := &imap.FetchItemBodySection{Peek: true, Part: []int{2}}
	sections := []*imap.FetchItemBodySection{inner, sig}
	var keyParts []*imap.BodyStructureSinglePart
	msg.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		single, ok := part.(*imap.BodyStructureSinglePart)
		if ok && strings.EqualFold(single.MediaType(), "application/pgp-keys") && single.Size <= partReadMax {
			sections = append(sections, &imap.FetchItemBodySection{Peek: true, Part: path})
			keyParts = append(keyParts, single)
		}
		return true
	})
	msgs, err := c.Fetch(imap.UIDSetNum(msg.UID), &imap.FetchOptions{UID: true, BodySection: sections}).Collect()
	if err != nil {
		return Verification{}, err
	}
	if len(msgs) == 0 {
		return Verification{}, alborz.NotFound("notfound.message", fmt.Sprint(msg.UID))
	}
	innerBytes := msgs[0].FindBodySection(inner)
	// A broken header only loses the protected From; the root's still names the authors.
	innerHeader, _ := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(innerBytes)))
	signature, err := io.ReadAll(transferDecoded(second.Encoding, bytes.NewReader(msgs[0].FindBodySection(sig))))
	if err != nil {
		return Verification{}, nil
	}
	var keys openpgp.EntityList
	for i, part := range keyParts {
		armored := transferDecoded(part.Encoding, bytes.NewReader(msgs[0].FindBodySection(sections[2+i])))
		if k, err := openpgp.ReadArmoredKeyRing(armored); err == nil {
			keys = append(keys, k...)
		}
	}

	check := p.verifier(ctx, client, signedMessage{header: msg.rootHeader, inner: innerHeader, protocol: protocol,
		sig: signature, keys: keys, from: envelopeSender(msg.Envelope)})
	if check == nil {
		return Verification{}, nil
	}
	var v Verification
	body := &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{{Peek: true, Part: []int{1}}}}
	err = streamSection(c, msg.UID, body, func(lit imap.LiteralReader) error {
		// The signed part as transmitted: its MIME header, then its body (RFC 1847 2.1).
		v = check(pgp.CRLFReader(io.MultiReader(bytes.NewReader(innerBytes), lit)))
		return nil
	})
	return v, err
}

// signedMessage is what a verdict needs of a signed message besides the signed part.
type signedMessage struct {
	// header is the root's, inner the signed part's, which carries the protected From.
	header, inner textproto.Header
	protocol      string
	sig           []byte
	// keys are the application/pgp-keys parts the message carries.
	keys openpgp.EntityList
	from string
}

// verifier readies the check the signed part streams through; nil when none can give a verdict.
func (p *plugin) verifier(ctx context.Context, client *http.Client, m signedMessage) func(signed io.Reader) Verification {
	authors := authorAddresses(m.header, m.inner)
	if smimeProtocols[m.protocol] {
		return smimeVerifier(m.sig, authors)
	}
	if m.protocol != pgpProtocol {
		return nil
	}
	domainKeys := p.domainKeys(ctx, client, authors)
	messageKeys := senderKeys(m.header, m.keys, m.from, authors)
	if len(domainKeys)+len(messageKeys) == 0 {
		return nil
	}
	claim := "pgp.claimmessage"
	if len(domainKeys) > 0 {
		claim = "pgp.claimmessageother"
	}
	return func(signed io.Reader) Verification {
		signer, err := openpgp.CheckArmoredDetachedSignature(slices.Concat(domainKeys, messageKeys), signed, bytes.NewReader(m.sig), nil)
		if err == nil {
			v := Verification{State: SignatureGood, Signer: identityOf(signer, authors, m.from), Source: KeyFromMessage, Claim: claim}
			if slices.ContainsFunc(domainKeys, func(k *openpgp.Entity) bool {
				return bytes.Equal(k.PrimaryKey.Fingerprint, signer.PrimaryKey.Fingerprint)
			}) {
				v.Source, v.Claim = KeyFromDomain, "pgp.claimdomain"
			}
			return v
		}
		// A signer comes back only when the bytes matched; an expired or revoked key, or another key, is silence.
		if signer == nil && !errors.Is(err, pgperrors.ErrUnknownIssuer) {
			return Verification{State: SignatureBad, Reason: reasonUnverified}
		}
		return Verification{}
	}
}

func withDelivery(v Verification, a *AuthResults) Verification {
	_, domain, _ := strings.Cut(v.Signer, "@")
	if v.State == SignatureGood && v.Source == KeyFromDomain && a != nil && a.Vouches(domain) {
		v.Confirmed = true
		v.Claim = "pgp.claimverified"
	}
	return v
}

func (p *plugin) domainKeys(ctx context.Context, client *http.Client, authors []string) openpgp.EntityList {
	var found openpgp.EntityList
	for _, author := range authors {
		for _, key := range p.wkd.keys(ctx, client, author) {
			if claimsAddress(key, authors) {
				found = append(found, key)
			}
		}
	}
	return found
}

// Only keys claiming an author count: another address's key says nothing about this message.
func senderKeys(h textproto.Header, attached openpgp.EntityList, from string, authors []string) openpgp.EntityList {
	found, _ := pgp.Autocrypt(h, from)
	for _, key := range attached {
		if claimsAddress(key, authors) {
			found = append(found, key)
		}
	}
	return found
}

// From alone is not enough, since mailing lists rewrite it: the signed protected From comes first.
func authorAddresses(h, inner textproto.Header) []string {
	var out []string
	add := func(value string) {
		for _, a := range strings.Split(value, ",") {
			if parsed, err := mail.ParseAddress(strings.TrimSpace(a)); err == nil {
				out = append(out, parsed.Address)
			}
		}
	}
	add(inner.Get("From"))
	add(h.Get("Reply-To"))
	add(h.Get("From"))
	return out
}

func claimsAddress(key *openpgp.Entity, authors []string) bool {
	for _, author := range authors {
		if author == "" {
			continue
		}
		for _, id := range key.Identities {
			if id.UserId != nil && strings.EqualFold(id.UserId.Email, author) {
				return true
			}
		}
	}
	return false
}

func identityOf(e *openpgp.Entity, authors []string, fallback string) string {
	for _, author := range authors {
		if claimsAddress(e, []string{author}) {
			return author
		}
	}
	return fallback
}

func messageRootHeader(msg *IMAPMessage) textproto.Header {
	return msg.rootHeader
}

func envelopeSender(env *imap.Envelope) string {
	if env == nil || len(env.From) == 0 {
		return ""
	}
	return env.From[0].Addr()
}

// FromDomain is the author's domain, for a rule about everyone there.
func (msg *IMAPMessage) FromDomain() string {
	from := envelopeSender(msg.Envelope)
	return from[strings.LastIndex(from, "@")+1:]
}
