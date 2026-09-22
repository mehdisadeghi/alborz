package alborzbase

import (
	"crypto/x509"
	"encoding/base64"
	"net/mail"
	"strings"

	"github.com/smallstep/pkcs7"
)

// S/MIME is the other half of what signed mail arrives as (ADR 43).
// Only the reading side is here: alborz holds no private key, so it
// signs nothing and decrypts nothing.

// smimeProtocols are the media types a multipart/signed carries a
// PKCS#7 signature as (RFC 8551 3.5); the x- name is what older
// clients write.
var smimeProtocols = map[string]bool{
	"application/pkcs7-signature":   true,
	"application/x-pkcs7-signature": true,
}

// verifySMIME checks a detached PKCS#7 signature over the part it
// covers. The outcomes are PGP's: the bytes and the signature
// disagreeing is the one thing reported bad; a certificate this system
// cannot chain, or one issued to somebody else, is an absence of
// evidence and says nothing at all.
func verifySMIME(signed, sig []byte, authors []string) Verification {
	p7, err := pkcs7.Parse(unarmour(sig))
	if err != nil {
		return Verification{}
	}
	p7.Content = signed
	if err := p7.Verify(); err != nil {
		return Verification{State: SignatureBad, Reason: reasonUnverified}
	}
	cert := p7.GetOnlySigner()
	if cert == nil {
		return Verification{}
	}
	signer := certAddress(cert, authors)
	if signer == "" {
		return Verification{}
	}
	intermediates := x509.NewCertPool()
	for _, c := range p7.Certificates {
		intermediates.AddCert(c)
	}
	// The authorities trusted for mail are not the authorities trusted
	// for the web, and a system store holds the second set. A chain
	// that does not build here is a certificate we cannot check, not
	// one that failed: the page says nothing, as it does for a PGP
	// signature with no key.
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:         smimeRoots(),
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
	}); err != nil {
		return Verification{}
	}
	return Verification{State: SignatureGood, Signer: signer, Source: KeyFromCA, Claim: "smime.claim"}
}

// smimeRoots are the authorities a certificate must chain to: nil, so
// that the system's own verifier answers. Naming a pool here instead
// would take macOS's verifier out of the picture, where the pool is
// empty and every chain would fail. Tests name their own.
var smimeRoots = func() *x509.CertPool { return nil }

// unarmour is the signature's bytes as DER: the part travels base64 in
// the message, and a server hands it over as it stands.
func unarmour(sig []byte) []byte {
	if der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(sig)), "")); err == nil {
		return der
	}
	return sig
}

// certAddress is the author address the certificate is issued to, or
// empty where it names somebody else. A certificate for another
// address says nothing about this message, however well it verifies -
// the same rule the PGP side applies to a key's identities.
func certAddress(cert *x509.Certificate, authors []string) string {
	named := append([]string{}, cert.EmailAddresses...)
	if addr, err := mail.ParseAddress(cert.Subject.CommonName); err == nil {
		named = append(named, addr.Address)
	}
	for _, claimed := range named {
		for _, author := range authors {
			if strings.EqualFold(claimed, author) {
				return claimed
			}
		}
	}
	return ""
}
