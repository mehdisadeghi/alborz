package pgp

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// A key with a few certifications is a few kilobytes.
const wkdMaxSize = 256 << 10

// zbase32 is the z-base-32 alphabet of WKD file names.
const zbase32 = "ybndrfg8ejkmcpqxot1uwisza345h769"

// WKD looks the address up in its domain's Web Key Directory
// (draft-koch-openpgp-webkey-service-22 3.1), keeping only the User IDs
// naming that address. A directory without the key gives no key and no error.
func WKD(ctx context.Context, c *http.Client, address string) (openpgp.EntityList, error) {
	local, domain, ok := strings.Cut(address, "@")
	if !ok || local == "" || domain == "" {
		return nil, fmt.Errorf("wkd: %q is not an address", address)
	}
	domain = asciiLower(domain)
	path := "/hu/" + hash(local) + "?l=" + url.QueryEscape(local)
	keys, err := fetch(ctx, c, "https://openpgpkey."+domain+"/.well-known/openpgpkey/"+domain+path)
	// The direct method is for a domain without the subdomain, not for one whose subdomain fails.
	if dns, ok := errors.AsType[*net.DNSError](err); ok && dns.IsNotFound {
		keys, err = fetch(ctx, c, "https://"+domain+"/.well-known/openpgpkey"+path)
	}
	if err != nil {
		return nil, err
	}
	var named openpgp.EntityList
	for _, key := range keys {
		for id, identity := range key.Identities {
			if identity.UserId == nil || !strings.EqualFold(identity.UserId.Email, address) {
				delete(key.Identities, id)
			}
		}
		if len(key.Identities) > 0 {
			named = append(named, key)
		}
	}
	return named, nil
}

func fetch(ctx context.Context, c *http.Client, u string) (openpgp.EntityList, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wkd: %s: %s", u, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, wkdMaxSize))
	if err != nil {
		return nil, err
	}
	return openpgp.ReadKeyRing(bytes.NewReader(body))
}

// hash is the local part's WKD name: only ASCII is lowercased, as the draft says.
func hash(local string) string {
	sum := sha1.Sum([]byte(asciiLower(local)))
	var out strings.Builder
	var acc, bits uint
	for _, b := range sum {
		acc = acc<<8 | uint(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out.WriteByte(zbase32[(acc>>bits)&31])
		}
	}
	return out.String()
}

func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}
