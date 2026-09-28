package mail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"git.mehdix.org/alborz/internal/pgp"
	"github.com/ProtonMail/go-crypto/openpgp"
	pgperrors "github.com/ProtonMail/go-crypto/openpgp/errors"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
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

func (p *plugin) verifySignature(ctx context.Context, client *http.Client, conn *imapclient.Client, mboxName string, uid imap.UID,
	bs imap.BodyStructure, rootHeader textproto.Header, from string) Verification {

	if !isSigned(bs) {
		return Verification{}
	}
	raw, _, err := fetchRawMessage(conn, mboxName, uid)
	if err != nil {
		return Verification{}
	}
	return p.verify(ctx, client, raw, rootHeader, from)
}

func (p *plugin) verify(ctx context.Context, client *http.Client, raw []byte, rootHeader textproto.Header, from string) Verification {
	signed, sig, protocol, err := pgp.Signed(raw)
	if err != nil {
		return Verification{}
	}
	authors := authorAddresses(rootHeader, signed)
	if smimeProtocols[protocol] {
		return verifySMIME(signed, sig, authors)
	}
	if protocol != pgpProtocol {
		return Verification{}
	}
	domainKeys := p.domainKeys(ctx, client, authors)
	messageKeys := senderKeys(rootHeader, raw, from, authors)
	claim := "pgp.claimmessage"
	if len(domainKeys) > 0 {
		claim = "pgp.claimmessageother"
	}

	bad := false
	for _, try := range []struct {
		keys   openpgp.EntityList
		source KeySource
		claim  string
	}{
		{domainKeys, KeyFromDomain, "pgp.claimdomain"},
		{messageKeys, KeyFromMessage, claim},
	} {
		if len(try.keys) == 0 {
			continue
		}
		signer, err := openpgp.CheckArmoredDetachedSignature(try.keys, bytes.NewReader(signed), bytes.NewReader(sig), nil)
		if err == nil {
			return Verification{State: SignatureGood, Signer: identityOf(signer, authors, from),
				Source: try.source, Claim: try.claim}
		}
		// A signer comes back only when the bytes matched; an expired or revoked key, or another key, is silence.
		if signer == nil && !errors.Is(err, pgperrors.ErrUnknownIssuer) {
			bad = true
		}
	}
	if bad {
		return Verification{State: SignatureBad, Reason: reasonUnverified}
	}
	return Verification{}
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
func senderKeys(h textproto.Header, raw []byte, from string, authors []string) openpgp.EntityList {
	found, _ := pgp.Autocrypt(h, from)
	for _, key := range attachedKeys(raw) {
		if claimsAddress(key, authors) {
			found = append(found, key)
		}
	}
	return found
}

// From alone is not enough, since mailing lists rewrite it: the signed protected From comes first.
func authorAddresses(h textproto.Header, signed []byte) []string {
	var out []string
	add := func(value string) {
		for _, a := range strings.Split(value, ",") {
			if parsed, err := mail.ParseAddress(strings.TrimSpace(a)); err == nil {
				out = append(out, parsed.Address)
			}
		}
	}
	if inner, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(signed))); err == nil {
		add(inner.Get("From"))
	}
	add(h.Get("Reply-To"))
	add(h.Get("From"))
	return out
}

func attachedKeys(raw []byte) openpgp.EntityList {
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	var keys openpgp.EntityList
	reader := entity.MultipartReader()
	if reader == nil {
		return nil
	}
	var walk func(message.MultipartReader)
	walk = func(r message.MultipartReader) {
		for {
			part, err := r.NextPart()
			if err != nil {
				return
			}
			if inner := part.MultipartReader(); inner != nil {
				walk(inner)
				continue
			}
			mediaType, _, err := part.Header.ContentType()
			if err != nil || !strings.EqualFold(mediaType, "application/pgp-keys") {
				continue
			}
			if k, err := openpgp.ReadArmoredKeyRing(part.Body); err == nil {
				keys = append(keys, k...)
			}
		}
	}
	walk(reader)
	return keys
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
