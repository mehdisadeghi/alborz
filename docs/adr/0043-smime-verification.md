# ADR 43: S/MIME signature verification

Status: accepted. Amends ADR 39.

## Context

Signed mail arrives as OpenPGP (RFC 3156) or S/MIME (RFC 8551)
`multipart/signed`. Only OpenPGP signatures were verified; ADR 39 makes
the S/MIME signature part of the message rather than a file.

## Decision

1. alborz MUST verify S/MIME signatures and MUST NOT sign or decrypt.
   Signing and decrypting require the reader's private key, which
   alborz does not hold.
2. Only the root `multipart/signed` is verified, and only when its
   `protocol` parameter matches the signature part's type and the
   signature part is within the part read limit (ADR 60).
3. Verdicts:
   - the digest does not match the content: bad, as in OpenPGP;
   - the digest matches, the signer certificate names an author
     address (`From` of the signed part, or `Reply-To`/`From` of the
     message) in its email addresses or Common Name, and it
     chains to a trusted root for `emailProtection`: good, claimed as
     "a certificate issued to <address> by an authority this server
     trusts";
   - anything else (foreign address, unchainable or expired
     certificate, no authenticated attributes): no verdict, as for an
     OpenPGP signature without a key.
4. Chain verification MUST use the platform verifier (`Roots: nil`),
   not `x509.SystemCertPool`, which is empty on macOS. Tests substitute
   their own pool.
5. Revocation (CRL, OCSP) is not checked, to avoid network calls when a
   message is opened.
6. The PKCS #7 parser MUST be fuzzed.
7. The verdict is fetched in its own request after the page loads,
   rendered outside the message frame; the signed part is streamed
   through the digest, never held.

## Consequences

- Signed mail costs one extra FETCH, on signed mail only.
- A certificate from an authority trusted for mail but not in the
  platform's roots gets no verdict. A mail trust list would be a
  separate decision.
- `smime.p7s` is not listed as a file (ADR 39); it remains in the raw
  message.

## References

- RFC 1847, RFC 5652, RFC 8551, RFC 3156
- ADR 39, ADR 60
- `mail/smime.go` (`smimeVerifier`, `smimeRoots`, `certAddress`),
  `mail/pgp.go` (`Verify`, `verifySignature`, `isSigned`),
  `mail/smime_test.go` (`FuzzSMIME`), `mail/web/signature.go`,
  route `mail.message.signature`
