package alborzbase

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"net/mail"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"
)

// A signature verdict has two outcomes worth showing and one worth
// staying quiet about. A message nobody signed says nothing, and so
// does one signed by a key we have no way to know: a mark on every
// message teaches people to ignore marks. Only "this is from who it
// says" and "this is not" are worth a reader's attention.
type SignatureState string

const (
	SignatureNone SignatureState = ""     // nothing signed, or no key to check with
	SignatureGood SignatureState = "good" // a signature that verifies, by a key that claims this sender
	SignatureBad  SignatureState = "bad"  // a signature that is present and does not verify
)

// Verification is what the page says about a message's authenticity. It is
// chrome, never body: the HTML sanitizer allows a style element, so a
// message can draw a convincing green band inside its own frame, and
// only a mark outside that frame means anything.
type Verification struct {
	State SignatureState
	// Signer is the address the verifying key claims, shown only when
	// the signature is good. A key that verifies for an address other
	// than the sender's is not a good signature; see verifySignature.
	Signer string
	// Reason names what went wrong, for a bad signature. It is a fixed
	// key, translated by the page, never the library's own English.
	Reason string
}

// reasonUnverified is the one reason a signature is reported bad: the
// bytes and the signature disagree. Every other outcome - no key, a key
// that claims somebody else - is an absence of evidence rather than
// evidence of forgery, and the page says nothing at all for those.
const reasonUnverified = "pgp.reasonunverified"

// pgpProtocol is the media type RFC 3156 3 gives the second part of a
// signed message; the first part is what the signature covers.
const pgpProtocol = "application/pgp-signature"

// signedParts finds the two parts of a PGP/MIME signed message and
// returns their IMAP part paths. A signed message is multipart/signed
// at some level with exactly two parts, so the walk stops at the first
// one it finds rather than guessing among several.
func signedParts(bs imap.BodyStructure) (content, sig []int, ok bool) {
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		if ok {
			return false
		}
		multi, isMulti := part.(*imap.BodyStructureMultiPart)
		if !isMulti || !strings.EqualFold(multi.Subtype, "signed") || len(multi.Children) != 2 {
			return true
		}
		second, isSingle := multi.Children[1].(*imap.BodyStructureSinglePart)
		if !isSingle || !strings.EqualFold(second.MediaType(), pgpProtocol) {
			return true
		}
		content = append(append([]int{}, path...), 1)
		sig = append(append([]int{}, path...), 2)
		ok = true
		return false
	})
	return content, sig, ok
}

// autocryptKey reads the sender's own key out of the Autocrypt header
// (Level 1, 2.1.1). It is the only key source that costs no storage and
// no network: the sender put it there. A keyring, WKD and an attached
// key would each be another source, and each is another decision about
// what to trust.
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

// verifySignature checks a PGP/MIME signature (RFC 3156 5) against the
// exact bytes the server holds.
//
// The signature covers the first body part's transmitted octets - its
// MIME headers, the blank line, and its body - so nothing here may
// re-serialise them. BODY[n.MIME] would be the tidy way to ask for them
// and is not safe: a server is free to rewrite a header it hands back
// that way, and the in-memory server the tests run against does. So the
// whole message is fetched once and the region is cut out of it by its
// boundary, which is the one thing every server passes through
// untouched.
func verifySignature(conn *imapclient.Client, mboxName string, uid imap.UID,
	bs imap.BodyStructure, rootHeader textproto.Header, from string) Verification {

	if _, _, ok := signedParts(bs); !ok {
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

	keys := senderKeys(rootHeader, raw, authorAddresses(rootHeader, signed))
	if len(keys) == 0 {
		// Signed, and nothing in the message says by whom. That is not
		// a failed signature and must not be shown as one: it is an
		// absence of evidence, so the page says nothing at all.
		return Verification{}
	}

	signer, err := openpgp.CheckArmoredDetachedSignature(
		keys, bytes.NewReader(signed), bytes.NewReader(sig), nil)
	if err != nil {
		return Verification{State: SignatureBad, Reason: reasonUnverified}
	}
	return Verification{State: SignatureGood, Signer: identityOf(signer, from)}
}

// senderKeys gathers the keys the message itself offers for its own
// sender, and only those: a key that claims a different address says
// nothing about this message, however valid its signature.
//
// Both sources are the sender's own doing - the Autocrypt header they
// set, and the public key they attached, which is what aerc and mutt
// send. Neither proves who they are; both prove that whoever composed
// the message holds the key, which is the whole of what the verdict
// claims. A keyring, WKD or a key server would each be a further
// decision about what to trust and is not made here.
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

// authorAddresses names who the message says wrote it. From alone is
// not the answer: a mailing list rewrites From to its own address to
// get past DMARC, so on every list message - which is where signed mail
// mostly lives - the author is somewhere else.
//
// The strongest answer is inside the signature. A signed part may carry
// the headers it protects (protected-headers="v1"), and a From there
// cannot be rewritten by anyone downstream without breaking the
// signature. Reply-To and the outer From follow it.
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

// attachedKeys reads every public key the message carries as a part.
// The parts are walked with go-message, which parses faithfully; it is
// re-serialising that loses bytes, and nothing is re-serialised here.
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
			// The part reader decodes the transfer encoding, so this is
			// the armoured key as the sender wrote it.
			if k, err := openpgp.ReadArmoredKeyRing(part.Body); err == nil {
				keys = append(keys, k...)
			}
		}
	}
	walk(reader)
	return keys
}

// claimsAddress says whether a key belongs to one of the addresses the
// message says wrote it. An Autocrypt header states its address itself;
// an attached key states it in its identities. Compared
// case-insensitively, because an address is.
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

// signedRegion cuts the signed octets and the armoured signature out of
// a raw message.
//
// The boundary is not guessed and not taken from a parsed structure: the
// line that opens the signature part is a boundary delimiter by
// construction (RFC 2046 5.1.1), so reading it off the bytes gives the
// boundary this message actually used, whatever nesting it sits in and
// whatever the headers claim.
func signedRegion(raw []byte) (signed, sig []byte, ok bool) {
	// Anchored on the armour, not on the media type: the media type is
	// also written in the enclosing part's protocol parameter, above the
	// body, and that copy is the one a forward search finds.
	sigStart := bytes.Index(raw, []byte("-----BEGIN PGP SIGNATURE-----"))
	if sigStart < 0 {
		return nil, nil, false
	}
	// The last line beginning "--" before the armour is the delimiter
	// that opens the signature part: a part's own headers never begin
	// that way.
	close := bytes.LastIndex(raw[:sigStart], []byte("\n--"))
	if close < 0 {
		return nil, nil, false
	}
	close++ // past the newline, at the first dash
	eol := bytes.IndexByte(raw[close:], '\n')
	if eol < 0 {
		return nil, nil, false
	}
	boundary := bytes.TrimRight(raw[close:close+eol], "\r")

	// The same delimiter, the first time it appears, opens the signed
	// part. Everything between the two is what was signed.
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
	// The CRLF before a delimiter belongs to the delimiter, not to the
	// part it closes (RFC 2046 5.1.1), so it is not signed.
	body := bytes.TrimSuffix(bytes.TrimSuffix(raw[from:close], []byte("\n")), []byte("\r"))

	sigEnd := bytes.Index(raw[sigStart:], []byte("-----END PGP SIGNATURE-----"))
	if sigEnd < 0 {
		return nil, nil, false
	}
	sigEnd += sigStart + len("-----END PGP SIGNATURE-----")

	return crlf(body), raw[sigStart:sigEnd], true
}

// indexFold finds needle in haystack without regard to case, which is
// what a media type comparison is.
func indexFold(haystack, needle []byte) int {
	return bytes.Index(bytes.ToLower(haystack), bytes.ToLower(needle))
}

// crlf gives every line the ending the signature was made over, without
// doubling one that is already there.
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

// identityOf names the signer for the page: the key's own identity when
// it has one, and the address the Autocrypt header claimed otherwise.
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

// messageRootHeader is the whole message's header, where Autocrypt and
// the list headers live; a part's own header has neither.
func messageRootHeader(msg *IMAPMessage) textproto.Header {
	return msg.rootHeader
}

// envelopeSender is the address the message claims to be from, which is
// the address a signature has to belong to.
func envelopeSender(env *imap.Envelope) string {
	if env == nil || len(env.From) == 0 {
		return ""
	}
	return env.From[0].Addr()
}
