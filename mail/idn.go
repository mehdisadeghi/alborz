package mail

// An address leaves with its domain in A-labels (RFC 5891 5), so a host
// without IDNA reads it; a local part in UTF-8 needs SMTPUTF8 (RFC 6531) and
// is written raw in the header (RFC 6532). See ADR 105.

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message/mail"
	"golang.org/x/net/idna"
)

// addressError is the address a refusal names; its text is the address alone, for the locale sentence.
type addressError struct {
	address string
	err     error
}

func (e addressError) Error() string { return e.address }

func (e addressError) Unwrap() error { return e.err }

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// aLabels parses address and writes its domain in A-labels, mapped as typed
// input is (UTS #46, the Lookup profile); a domain in ASCII is left as typed.
func aLabels(address string) (*mail.Address, error) {
	addr, err := mail.ParseAddress(address)
	if err != nil {
		return nil, fmt.Errorf("parsing address %q failed: %v", address, err)
	}
	at := strings.LastIndexByte(addr.Address, '@')
	if at < 0 || isASCII(addr.Address[at+1:]) {
		return addr, nil
	}
	domain, err := idna.Lookup.ToASCII(addr.Address[at+1:])
	if err != nil {
		return nil, Refused{"form.addressinvalid", addressError{addr.Address, err}}
	}
	addr.Address = addr.Address[:at+1] + domain
	return addr, nil
}

func aLabelList(addresses []string) ([]*mail.Address, error) {
	l := make([]*mail.Address, len(addresses))
	for i, s := range addresses {
		addr, err := aLabels(s)
		if err != nil {
			return nil, err
		}
		l[i] = addr
	}
	return l, nil
}

// needsUTF8 is the first of addresses, as typed, whose addr-spec stays in UTF-8 after aLabels, "" for none.
func needsUTF8(addresses []string, addrs []*mail.Address) string {
	for i, a := range addrs {
		if !isASCII(a.Address) {
			typed, _ := mail.ParseAddress(addresses[i])
			return typed.Address
		}
	}
	return ""
}
