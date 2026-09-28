package mail

import (
	"crypto/x509"
	"encoding/base64"
	"net/mail"
	"strings"

	"github.com/smallstep/pkcs7"
)

// Only the reading side of S/MIME is here: alborz holds no private key (ADR 43).

var smimeProtocols = map[string]bool{
	"application/pkcs7-signature":   true,
	"application/x-pkcs7-signature": true,
}

// Only bytes disagreeing with the signature are bad; an unchainable or foreign certificate says nothing.
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
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:         smimeRoots(),
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
	}); err != nil {
		return Verification{}
	}
	return Verification{State: SignatureGood, Signer: signer, Source: KeyFromCA, Claim: "smime.claim"}
}

// nil lets the platform verifier answer; tests substitute their own pool.
var smimeRoots = func() *x509.CertPool { return nil }

func unarmour(sig []byte) []byte {
	if der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(sig)), "")); err == nil {
		return der
	}
	return sig
}

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
