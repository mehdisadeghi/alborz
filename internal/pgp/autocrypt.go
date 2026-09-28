package pgp

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/emersion/go-message/textproto"
)

// Autocrypt reads the sender's key from the Autocrypt header (Level 1),
// valid only for the From address. No header gives no key and no error.
func Autocrypt(h textproto.Header, from string) (openpgp.EntityList, error) {
	var found openpgp.EntityList
	var valid int
	var errs []error
	for _, value := range h.Values("Autocrypt") {
		keys, err := autocrypt(value, from)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		found, valid = keys, valid+1
	}
	switch valid {
	case 0:
		return nil, errors.Join(errs...)
	case 1:
		return found, nil
	}
	return nil, errors.New("autocrypt: more than one valid header")
}

func autocrypt(value, from string) (openpgp.EntityList, error) {
	var addr, keydata string
	for attr := range strings.SplitSeq(value, ";") {
		attr = strings.TrimSpace(attr)
		if attr == "" {
			continue
		}
		name, val, found := strings.Cut(attr, "=")
		if !found {
			return nil, fmt.Errorf("autocrypt: attribute %q has no value", attr)
		}
		switch name = strings.ToLower(strings.TrimSpace(name)); {
		case name == "addr":
			addr = strings.TrimSpace(val)
		case name == "keydata":
			// The header is folded; the base64 carries no spaces.
			keydata = strings.Join(strings.Fields(val), "")
		case name == "prefer-encrypt", strings.HasPrefix(name, "_"):
		default:
			return nil, fmt.Errorf("autocrypt: unknown critical attribute %q", name)
		}
	}
	if addr == "" || keydata == "" {
		return nil, errors.New("autocrypt: addr or keydata missing")
	}
	if !strings.EqualFold(addr, from) {
		return nil, fmt.Errorf("autocrypt: addr %s is not the From address", addr)
	}
	der, err := base64.StdEncoding.DecodeString(keydata)
	if err != nil {
		return nil, fmt.Errorf("autocrypt keydata is not base64: %v", err)
	}
	keys, err := openpgp.ReadKeyRing(bytes.NewReader(der))
	if err != nil {
		return nil, fmt.Errorf("autocrypt keydata is not a key: %v", err)
	}
	return keys, nil
}
