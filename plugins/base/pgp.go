package alborzbase

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
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

func signedParts(bs imap.BodyStructure) (content, sig []int, ok bool) {
	content, sig, _, ok = signedPartsOf(bs)
	return content, sig, ok
}

func signedPartsOf(bs imap.BodyStructure) (content, sig []int, protocol string, ok bool) {
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		if ok {
			return false
		}
		multi, isMulti := part.(*imap.BodyStructureMultiPart)
		if !isMulti || !strings.EqualFold(multi.Subtype, "signed") || len(multi.Children) != 2 {
			return true
		}
		second, isSingle := multi.Children[1].(*imap.BodyStructureSinglePart)
		if !isSingle {
			return true
		}
		media := strings.ToLower(second.MediaType())
		if media != pgpProtocol && !smimeProtocols[media] {
			return true
		}
		protocol = media
		content = append(append([]int{}, path...), 1)
		sig = append(append([]int{}, path...), 2)
		ok = true
		return false
	})
	return content, sig, protocol, ok
}

func autocryptKey(h textproto.Header) (openpgp.EntityList, string, error) {
	value := h.Get("Autocrypt")
	if value == "" {
		return nil, "", nil
	}
	var addr, keydata string
	for _, attr := range strings.Split(value, ";") {
		name, val, found := strings.Cut(strings.TrimSpace(attr), "=")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "addr":
			addr = strings.TrimSpace(val)
		case "keydata":
			// The header is folded; the base64 carries no spaces.
			keydata = strings.Join(strings.Fields(val), "")
		}
	}
	if keydata == "" {
		return nil, "", nil
	}
	der, err := base64.StdEncoding.DecodeString(keydata)
	if err != nil {
		return nil, "", fmt.Errorf("autocrypt keydata is not base64: %v", err)
	}
	keys, err := openpgp.ReadKeyRing(bytes.NewReader(der))
	if err != nil {
		return nil, "", fmt.Errorf("autocrypt keydata is not a key: %v", err)
	}
	return keys, addr, nil
}

// The signed octets are cut from the raw message, never re-serialised, so they match byte for byte.
func (p *plugin) verifySignature(client *http.Client, conn *imapclient.Client, mboxName string, uid imap.UID,
	bs imap.BodyStructure, rootHeader textproto.Header, from string) Verification {

	_, _, protocol, ok := signedPartsOf(bs)
	if !ok {
		return Verification{}
	}

	raw, _, err := fetchRawMessage(conn, mboxName, uid)
	if err != nil {
		return Verification{}
	}
	signed, sig, ok := signedRegion(raw)
	if !ok {
		return Verification{}
	}

	authors := authorAddresses(rootHeader, signed)
	if smimeProtocols[protocol] {
		return verifySMIME(signed, sig, authors)
	}
	domainKeys := p.domainKeys(client, authors)
	messageKeys := senderKeys(rootHeader, raw, authors)
	if len(domainKeys)+len(messageKeys) == 0 {
		// Signed, but by nobody named: absence of evidence, not a bad signature.
		return Verification{}
	}

	check := func(keys openpgp.EntityList) (*openpgp.Entity, error) {
		if len(keys) == 0 {
			return nil, errors.New("no key")
		}
		return openpgp.CheckArmoredDetachedSignature(
			keys, bytes.NewReader(signed), bytes.NewReader(sig), nil)
	}
	if signer, err := check(domainKeys); err == nil {
		return Verification{State: SignatureGood, Signer: identityOf(signer, from),
			Source: KeyFromDomain, Claim: "pgp.claimdomain"}
	}
	if signer, err := check(messageKeys); err == nil {
		claim := "pgp.claimmessage"
		if len(domainKeys) > 0 {
			claim = "pgp.claimmessageother"
		}
		return Verification{State: SignatureGood, Signer: identityOf(signer, from),
			Source: KeyFromMessage, Claim: claim}
	}
	return Verification{State: SignatureBad, Reason: reasonUnverified}
}

func withDelivery(v Verification, a *AuthResults) Verification {
	_, domain, _ := strings.Cut(v.Signer, "@")
	if v.State == SignatureGood && v.Source == KeyFromDomain && a != nil && a.Vouches(domain) {
		v.Confirmed = true
		v.Claim = "pgp.claimverified"
	}
	return v
}

func (p *plugin) domainKeys(client *http.Client, authors []string) openpgp.EntityList {
	var found openpgp.EntityList
	for _, author := range authors {
		for _, key := range p.wkd.keys(client, author) {
			if claimsAddress(key, "", authors) {
				found = append(found, key)
			}
		}
	}
	return found
}

// Only keys claiming an author count: another address's key says nothing about this message.
func senderKeys(h textproto.Header, raw []byte, authors []string) openpgp.EntityList {
	var found openpgp.EntityList
	if keys, addr, err := autocryptKey(h); err == nil {
		for _, key := range keys {
			if claimsAddress(key, addr, authors) {
				found = append(found, key)
			}
		}
	}
	for _, key := range attachedKeys(raw) {
		if claimsAddress(key, "", authors) {
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

func claimsAddress(key *openpgp.Entity, stated string, authors []string) bool {
	for _, author := range authors {
		if author == "" {
			continue
		}
		if stated != "" && strings.EqualFold(stated, author) {
			return true
		}
		for _, id := range key.Identities {
			if id.UserId != nil && strings.EqualFold(id.UserId.Email, author) {
				return true
			}
		}
	}
	return false
}

func signedRegion(raw []byte) (signed, sig []byte, ok bool) {
	// Anchored on the armour: the media type also appears earlier, in the protocol parameter.
	sigStart := bytes.Index(raw, []byte("-----BEGIN PGP SIGNATURE-----"))
	if sigStart < 0 {
		return nil, nil, false
	}
	// A part's own headers never begin with "--", so the last such line opens the signature part.
	close := bytes.LastIndex(raw[:sigStart], []byte("\n--"))
	if close < 0 {
		return nil, nil, false
	}
	close++
	eol := bytes.IndexByte(raw[close:], '\n')
	if eol < 0 {
		return nil, nil, false
	}
	boundary := bytes.TrimRight(raw[close:close+eol], "\r")

	open := bytes.Index(raw, append([]byte("\n"), boundary...))
	if open < 0 || open >= close {
		return nil, nil, false
	}
	from := open + 1 + len(boundary)
	for from < len(raw) && (raw[from] == '\r' || raw[from] == '\n') {
		from++
		if raw[from-1] == '\n' {
			break
		}
	}
	if from > close {
		return nil, nil, false
	}
	// The CRLF before a delimiter belongs to the delimiter (RFC 2046 5.1.1).
	body := bytes.TrimSuffix(bytes.TrimSuffix(raw[from:close], []byte("\n")), []byte("\r"))

	sigEnd := bytes.Index(raw[sigStart:], []byte("-----END PGP SIGNATURE-----"))
	if sigEnd < 0 {
		return nil, nil, false
	}
	sigEnd += sigStart + len("-----END PGP SIGNATURE-----")

	return crlf(body), raw[sigStart:sigEnd], true
}

func crlf(b []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(b) + len(b)/16)
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' && (i == 0 || b[i-1] != '\r') {
			out.WriteByte('\r')
		}
		out.WriteByte(b[i])
	}
	return out.Bytes()
}

func identityOf(e *openpgp.Entity, fallback string) string {
	if e != nil {
		for _, id := range e.Identities {
			if id.UserId != nil && id.UserId.Email != "" {
				return id.UserId.Email
			}
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
