package mail

import (
	"context"
	"fmt"

	"git.mehdix.org/alborz"
)

const settingsKey = "base.settings"

const (
	// Named for the warming code, which has no context to read a profile from.
	defaultMessagesPerPage = alborz.DefaultMessagesPerPage
	maxSignature           = 2048
	maxSignatures          = 20
	maxSignatureName       = 60
	maxFullName            = 512
)

// Signature is one of an account's sign-offs, written under the "-- " delimiter.
type Signature struct {
	Name string
	Text string
}

type Settings struct {
	// Signatures belong to the account, not an identity: the writer picks the persona per message.
	Signatures []Signature
	// DefaultSignature names the one a new message starts with; empty means none.
	DefaultSignature string
	From             string
	// Identities are the other addresses this mailbox may send as, bare or "Name <address>".
	Identities    []string
	Subscriptions []string

	// TrustedAuthServ is the authserv-id whose Authentication-Results verdict is read; empty uses SuggestAuthServ.
	TrustedAuthServ string

	// IndexedSearch is the reader stating the server has a full-text index it does not announce.
	IndexedSearch bool
}

func LoadSettings(ctx context.Context, s alborz.Store) (*Settings, error) {
	settings := &Settings{}
	if err := s.Get(ctx, settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	if key, limit := settings.check(); key != "" {
		return nil, fmt.Errorf("stored settings break %s (%d)", key, limit)
	}
	return settings, nil
}

func (s *Settings) check() (string, int) {
	switch {
	case len(s.Signatures) > maxSignatures:
		return "form.signaturecount", maxSignatures
	case len(s.From) > maxFullName:
		return "form.namelong", maxFullName
	}
	for _, sig := range s.Signatures {
		if len(sig.Text) > maxSignature {
			return "form.signaturelong", maxSignature
		}
		if len(sig.Name) > maxSignatureName {
			return "form.signaturenamelong", maxSignatureName
		}
	}
	return "", 0
}

func (s *Settings) signatureNamed(name string) (Signature, bool) {
	for _, sig := range s.Signatures {
		if sig.Name == name {
			return sig, true
		}
	}
	return Signature{}, false
}
