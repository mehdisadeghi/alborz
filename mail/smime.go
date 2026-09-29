package mail

import (
	"crypto"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"hash"
	"io"
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
func smimeVerifier(sig []byte, authors []string) func(signed io.Reader) Verification {
	p7, err := pkcs7.Parse(unarmour(sig))
	if err != nil {
		return nil
	}
	// Content hashed as it streams by is checked against the digest the signer
	// signed (RFC 5652 5.4); a signature over the content itself would need it held.
	for _, s := range p7.Signers {
		if len(s.AuthenticatedAttributes) == 0 {
			return nil
		}
	}
	return func(signed io.Reader) Verification {
		sums, err := digestsOf(signed)
		if err != nil {
			return Verification{}
		}
		p7.Hasher = sums
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
}

// digests are the content's hashes by every algorithm a signer may name, taken in one pass.
type digests map[crypto.Hash][]byte

func digestsOf(r io.Reader) (digests, error) {
	hashes := map[crypto.Hash]hash.Hash{
		crypto.SHA1: sha1.New(), crypto.SHA256: sha256.New(), crypto.SHA384: sha512.New384(), crypto.SHA512: sha512.New(),
	}
	var all []io.Writer
	for _, h := range hashes {
		all = append(all, h)
	}
	if _, err := io.Copy(io.MultiWriter(all...), r); err != nil {
		return nil, err
	}
	sums := digests{}
	for alg, h := range hashes {
		sums[alg] = h.Sum(nil)
	}
	return sums, nil
}

// Hash gives pkcs7 the digest already taken, as the content has streamed past.
func (d digests) Hash(alg crypto.Hash, _ io.Reader) ([]byte, error) {
	return d[alg], nil
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
