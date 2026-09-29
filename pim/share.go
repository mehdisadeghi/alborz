package pim

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	netmail "net/mail"
	"slices"
	"strings"

	"git.mehdix.org/alborz/pim/store"
)

// The refusals of an address to share with.
var (
	ErrNotAnAddress = errors.New("not a mail address")
	ErrNotServed    = errors.New("no domain served here")
	ErrOwnAddress   = errors.New("the owner's own address")
)

// Invitee is the account typed to share with: an address of a domain in served, and not own.
func Invitee(typed string, served []string, own string) (string, error) {
	addr, err := netmail.ParseAddress(strings.TrimSpace(typed))
	if err != nil {
		return "", ErrNotAnAddress
	}
	to := strings.ToLower(addr.Address)
	_, domain, _ := strings.Cut(to, "@")
	// The unnamed domain takes sign-ins from anywhere.
	if !slices.Contains(served, domain) && !slices.Contains(served, "") {
		return "", ErrNotServed
	}
	if to == strings.ToLower(own) {
		return "", ErrOwnAddress
	}
	return to, nil
}

// Share keeps sh; changing a share keeps a yes already given, and sharing again after a no is a new invitation.
func Share(st *store.Store, sh store.Share) (store.Share, error) {
	if was, err := st.Share(sh.Ref(), sh.To); err == nil && was.Accepted {
		sh.Accepted, sh.Created = true, was.Created
	}
	return sh, st.PutShare(sh)
}

// 192 bits: past guessing, short enough to paste.
const publicSecretBytes = 24

// Publish gives the collection a public address, or takes it away; publishing again makes a new one.
func Publish(st *store.Store, ref store.Ref, publish bool) error {
	secret := ""
	if publish {
		raw := make([]byte, publicSecretBytes)
		rand.Read(raw)
		secret = base64.RawURLEncoding.EncodeToString(raw)
	}
	return st.Update(ref, func(c *store.Collection) { c.Public = secret })
}
