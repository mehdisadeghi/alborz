package alborzbase

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"testing"
	"time"

	"github.com/smallstep/pkcs7"
)

// signer mints a certificate for an address, issued by a CA of its
// own, and the detached signature it writes over some bytes.
func signer(t testing.TB, address string) (root *x509.Certificate, sign func([]byte) []byte) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Rig authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:   big.NewInt(2),
		Subject:        pkix.Name{CommonName: address},
		EmailAddresses: []string{address},
		NotBefore:      time.Now().Add(-time.Hour),
		NotAfter:       time.Now().Add(time.Hour),
		KeyUsage:       x509.KeyUsageDigitalSignature,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}

	return ca, func(content []byte) []byte {
		signed, err := pkcs7.NewSignedData(content)
		if err != nil {
			t.Fatal(err)
		}
		if err := signed.AddSigner(leaf, key, pkcs7.SignerInfoConfig{}); err != nil {
			t.Fatal(err)
		}
		signed.Detach()
		der, err := signed.Finish()
		if err != nil {
			t.Fatal(err)
		}
		return der
	}
}

// What an S/MIME signature is allowed to claim: that these bytes come
// from the address its certificate names, and only where the issuer is
// one this server trusts. Everything else says nothing rather than
// showing a mark the reader cannot check (ADR 43).
func TestSMIMESaysOnlyWhatItChecked(t *testing.T) {
	const body = "Content-Type: text/plain\r\n\r\nThe signed words.\r\n"
	ca, sign := signer(t, "gil@example.org")
	trusted := x509.NewCertPool()
	trusted.AddCert(ca)
	smimeRoots = func() *x509.CertPool { return trusted }
	t.Cleanup(func() { smimeRoots = func() *x509.CertPool { return nil } })

	sig := sign([]byte(body))
	authors := []string{"gil@example.org"}

	if v := verifySMIME([]byte(body), sig, authors); v.State != SignatureGood ||
		v.Signer != "gil@example.org" || v.Source != KeyFromCA {
		t.Errorf("a signature by a trusted issuer for the sender reads %+v", v)
	}

	// The part travels base64 in the message, and a server may hand it
	// over as it stands.
	if v := verifySMIME([]byte(body), []byte(wrapped(sig)), authors); v.State != SignatureGood {
		t.Errorf("the same signature in base64 reads %+v", v)
	}

	tampered := body + "One line more.\r\n"
	if v := verifySMIME([]byte(tampered), sig, authors); v.State != SignatureBad {
		t.Errorf("a signature over other bytes reads %+v, want bad", v)
	}

	if v := verifySMIME([]byte(body), sig, []string{"eve@example.org"}); v.State != SignatureNone {
		t.Errorf("a certificate issued to somebody else reads %+v, want nothing", v)
	}

	smimeRoots = func() *x509.CertPool { return x509.NewCertPool() }
	if v := verifySMIME([]byte(body), sig, authors); v.State != SignatureNone {
		t.Errorf("an issuer this server cannot check reads %+v, want nothing", v)
	}
}

// wrapped is the signature as a message carries it: base64 in lines.
func wrapped(der []byte) string {
	const line = 76
	encoded := base64.StdEncoding.EncodeToString(der)
	var out string
	for len(encoded) > line {
		out += encoded[:line] + "\r\n"
		encoded = encoded[line:]
	}
	return out + encoded + "\r\n"
}

// The signature comes from whoever sent the message, so the parser
// reads bytes nobody vouches for. Any verdict is acceptable; a panic
// is not.
func FuzzSMIME(f *testing.F) {
	_, sign := signer(f, "gil@example.org")
	sig := sign([]byte("body"))
	f.Add(sig)
	f.Add(sig[:len(sig)/2])
	f.Add([]byte("not a signature at all"))
	f.Fuzz(func(t *testing.T, sig []byte) {
		verifySMIME([]byte("body"), sig, []string{"gil@example.org"})
	})
}
