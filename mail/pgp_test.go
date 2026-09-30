package mail

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz/internal/pgp"
	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
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

func pgpSignature(t *testing.T, entity *openpgp.Entity, signed string, config *packet.Config) string {
	t.Helper()
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, entity, strings.NewReader(signed), config); err != nil {
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
	signature := pgpSignature(t, key, signedWords, nil)
	var armoredKey bytes.Buffer
	w, err := armor.Encode(&armoredKey, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := key.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	// Armor ends its lines in LF alone; a sender signs the canonical CRLF.
	withKey := string(pgp.CRLF([]byte("Content-Type: multipart/mixed; boundary=in\r\n\r\n--in\r\n" + signedWords +
		"--in\r\nContent-Type: application/pgp-keys\r\n\r\n" + armoredKey.String() + "\r\n--in--\r\n")))

	past := time.Now().Add(-48 * time.Hour)
	expired := pgpKey(t, &packet.Config{Time: func() time.Time { return past }, KeyLifetimeSecs: 3600})
	expiredSignature := pgpSignature(t, expired, signedWords, &packet.Config{Time: func() time.Time { return past.Add(time.Minute) }})

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
		{"key attached beside the signed words", mailFrom("", multipartSigned(pgpProtocol, withKey, pgpSignature(t, key, withKey, nil))), SignatureGood},
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
			v := verdictOver(t, c.raw)
			if v.State != c.want {
				t.Fatalf("verdict %+v, want %q", v, c.want)
			}
			if v.State == SignatureGood && v.Signer != "gil@example.org" {
				t.Fatalf("signer %q", v.Signer)
			}
		})
	}
}

// Over IMAP, as the check reads the signed part the way the server cuts it.
func verdictOver(t *testing.T, raw string) Verification {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("a@test.local", "x")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := user.Append("INBOX", strings.NewReader(raw), &imap.AppendOptions{}); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
	})
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	c, err := imapclient.DialInsecure(ln.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("a@test.local", "x").Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	msgs, err := c.Fetch(imap.UIDSetNum(1), partFetchOptions(nil, true, false)).Collect()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("fetch: %v", err)
	}
	msg, _, err := messagePart(msgs[0], "INBOX", 1, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	p := &Mailboxes{wkd: newWKDCache(0)}
	v, err := p.verifySignature(context.Background(), &http.Client{Transport: unresolvable{}}, c, msg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestWKDCacheStaysBounded(t *testing.T) {
	w := newWKDCache(wkdKeepFor)
	client := &http.Client{Transport: unresolvable{}}
	for i := range maxWKDEntries + 10 {
		w.keys(context.Background(), client, fmt.Sprintf("a%d@example.org", i))
	}
	if n := len(w.entries); n != maxWKDEntries {
		t.Errorf("reading %d authors kept %d entries; the cap is %d", maxWKDEntries+10, n, maxWKDEntries)
	}
	if _, ok := w.entries[fmt.Sprintf("a%d@example.org", maxWKDEntries+9)]; !ok {
		t.Error("the newest author was evicted")
	}
}
