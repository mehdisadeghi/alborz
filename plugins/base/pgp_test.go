package alborzbase

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/emersion/go-message/textproto"
)

// signedFixture builds a PGP/MIME message and signs it with a key made
// here, so the test proves the verifier agrees with an implementation
// rather than with a stored blob nobody can regenerate.
func signedFixture(t *testing.T) (raw []byte, header textproto.Header) {
	t.Helper()
	entity, err := openpgp.NewEntity("Gil", "test", "gil@example.org", nil)
	if err != nil {
		t.Fatal(err)
	}
	part := "Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		"One part, signed.\r\n"

	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, entity, strings.NewReader(part), nil); err != nil {
		t.Fatal(err)
	}
	var key bytes.Buffer
	if err := entity.Serialize(&key); err != nil {
		t.Fatal(err)
	}

	autocrypt := "Autocrypt: addr=gil@example.org; keydata=" +
		base64.StdEncoding.EncodeToString(key.Bytes()) + "\r\n"
	raw = []byte(fmt.Sprintf(
		"From: Gil <gil@example.org>\r\nTo: a@test.local\r\nSubject: Signed\r\n"+
			"MIME-Version: 1.0\r\n%s"+
			"Content-Type: multipart/signed; micalg=pgp-sha512; "+
			"protocol=\"application/pgp-signature\"; boundary=sig\r\n\r\n"+
			"--sig\r\n%s\r\n"+
			"--sig\r\nContent-Type: application/pgp-signature\r\n\r\n%s\r\n"+
			"--sig--\r\n",
		autocrypt, part, sig.String()))

	header, err = textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return raw, header
}

// TestListRewrittenFrom is the case that matters most in practice:
// a mailing list rewrote From to its own address to get past DMARC, so
// the key claims an address the message no longer says it is from. The
// author is in the protected headers inside the signature and in
// Reply-To, and a rule that looks only at From drops every list message
// - which is where signed mail mostly lives.
func TestListRewrittenFrom(t *testing.T) {
	raw, header := signedFixture(t)
	rewritten := bytes.Replace(raw,
		[]byte("From: Gil <gil@example.org>"),
		[]byte("From: Gil via List <list@lists.example.org>\r\nReply-To: gil@example.org"), 1)
	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(rewritten)))
	if err != nil {
		t.Fatal(err)
	}
	_ = header

	signed, _, ok := signedRegion(rewritten)
	if !ok {
		t.Fatal("signed region not found")
	}
	authors := authorAddresses(h, signed)
	if len(authors) == 0 {
		t.Fatal("no author addresses")
	}
	if keys := senderKeys(h, rewritten, authors); len(keys) == 0 {
		t.Fatalf("the sender's key was dropped; authors were %v", authors)
	}

	// A key that belongs to nobody the message names is still refused.
	if keys := senderKeys(h, rewritten, []string{"someone@else.example"}); len(keys) != 0 {
		t.Fatal("a key claiming another address was accepted")
	}
}

// TestSignatureVerifies is the whole point: the signed octets have to
// be reproduced byte for byte, and a single byte changed anywhere in
// them has to be caught.
func TestSignatureVerifies(t *testing.T) {
	raw, header := signedFixture(t)

	keys, addr, err := autocryptKey(header)
	if err != nil || len(keys) == 0 {
		t.Fatalf("autocrypt key not read: %v", err)
	}
	if addr != "gil@example.org" {
		t.Fatalf("autocrypt addr = %q", addr)
	}

	authors := authorAddresses(header, nil)
	if len(senderKeys(header, raw, authors)) == 0 {
		t.Fatal("the sender's own key was not found")
	}

	signed, sig, ok := signedRegion(raw)
	if !ok {
		t.Fatal("signed region not found")
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(
		keys, bytes.NewReader(signed), bytes.NewReader(sig), nil); err != nil {
		t.Fatalf("a good signature did not verify: %v\nsigned region was %q", err, signed)
	}

	// One byte changed in the signed part, nothing else touched.
	tampered := bytes.Replace(raw, []byte("One part, signed."), []byte("One part, sigued."), 1)
	signed, sig, ok = signedRegion(tampered)
	if !ok {
		t.Fatal("signed region not found after tampering")
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(
		keys, bytes.NewReader(signed), bytes.NewReader(sig), nil); err == nil {
		t.Fatal("a tampered message verified")
	}
}
