package mail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/emersion/go-message/textproto"
)

const signedWords = "Content-Type: text/plain; charset=UTF-8\r\n\r\nOne part, signed.\r\n"

func pgpKey(t *testing.T, config *packet.Config) *openpgp.Entity {
	t.Helper()
	entity, err := openpgp.NewEntity("Gil", "test", "gil@example.org", config)
	if err != nil {
		t.Fatal(err)
	}
	return entity
}

func pgpSignature(t *testing.T, entity *openpgp.Entity, config *packet.Config) string {
	t.Helper()
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, entity, strings.NewReader(signedWords), config); err != nil {
		t.Fatal(err)
	}
	return "Content-Type: application/pgp-signature\r\n\r\n" + sig.String()
}

func autocryptFor(t *testing.T, entity *openpgp.Entity) string {
	t.Helper()
	var key bytes.Buffer
	if err := entity.Serialize(&key); err != nil {
		t.Fatal(err)
	}
	return "Autocrypt: addr=gil@example.org; keydata=" + base64.StdEncoding.EncodeToString(key.Bytes()) + "\r\n"
}

func multipartSigned(protocol, part, sigPart string) string {
	return "Content-Type: multipart/signed; micalg=sha256; protocol=\"" + protocol + "\"; boundary=sig\r\n\r\n" +
		"--sig\r\n" + part + "\r\n--sig\r\n" + sigPart + "\r\n--sig--\r\n"
}

func mailFrom(extra, body string) string {
	return "From: Gil <gil@example.org>\r\nTo: a@test.local\r\nSubject: Signed\r\nMIME-Version: 1.0\r\n" + extra + body
}

type unresolvable struct{}

func (unresolvable) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, &net.DNSError{Name: r.URL.Hostname(), IsNotFound: true}
}

func TestVerdict(t *testing.T) {
	key := pgpKey(t, nil)
	carried := autocryptFor(t, key)
	signature := pgpSignature(t, key, nil)

	past := time.Now().Add(-48 * time.Hour)
	expired := pgpKey(t, &packet.Config{Time: func() time.Time { return past }, KeyLifetimeSecs: 3600})
	expiredSignature := pgpSignature(t, expired, &packet.Config{Time: func() time.Time { return past.Add(time.Minute) }})

	ca, sign := signer(t, "gil@example.org")
	trusted := x509.NewCertPool()
	trusted.AddCert(ca)
	smimeRoots = func() *x509.CertPool { return trusted }
	t.Cleanup(func() { smimeRoots = func() *x509.CertPool { return nil } })
	smimeSignature := "Content-Type: application/pkcs7-signature; name=smime.p7s\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + wrapped(sign([]byte(signedWords)))

	for _, c := range []struct {
		name string
		raw  string
		want SignatureState
	}{
		{"root PGP/MIME", mailFrom(carried, multipartSigned(pgpProtocol, signedWords, signature)), SignatureGood},
		{"tampered", mailFrom(carried, multipartSigned(pgpProtocol, strings.Replace(signedWords, "signed", "sigued", 1), signature)), SignatureBad},
		{"signed by a key the sender did not carry", mailFrom(autocryptFor(t, pgpKey(t, nil)), multipartSigned(pgpProtocol, signedWords, signature)), SignatureNone},
		{"expired key", mailFrom(autocryptFor(t, expired), multipartSigned(pgpProtocol, signedWords, expiredSignature)), SignatureNone},
		{"signed part copied into unsigned mail", mailFrom(carried,
			"Content-Type: multipart/mixed; boundary=outer\r\n\r\n"+
				"--outer\r\nContent-Type: text/plain\r\n\r\nPay the new account.\r\n"+
				"--outer\r\n"+multipartSigned(pgpProtocol, signedWords, signature)+
				"\r\n--outer--\r\n"), SignatureNone},
		{"root S/MIME", mailFrom("", multipartSigned("application/pkcs7-signature", signedWords, smimeSignature)), SignatureGood},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, err := textproto.ReadHeader(bufio.NewReader(strings.NewReader(c.raw)))
			if err != nil {
				t.Fatal(err)
			}
			p := &plugin{wkd: newWKDCache(0)}
			v := p.verify(context.Background(), &http.Client{Transport: unresolvable{}}, []byte(c.raw), h, "gil@example.org")
			if v.State != c.want {
				t.Fatalf("verdict %+v, want %q", v, c.want)
			}
			if v.State == SignatureGood && v.Signer != "gil@example.org" {
				t.Fatalf("signer %q", v.Signer)
			}
		})
	}
}
