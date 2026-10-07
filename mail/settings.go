package mail

import (
	"context"
	"slices"

	"git.mehdix.org/alborz"
)

const settingsKey = "base.settings"

const (
	// Named for the warming code, which has no context to read a profile from.
	defaultMessagesPerPage = alborz.DefaultMessagesPerPage
	maxSignature           = 2048
	MaxSignatures          = 20
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

	// Unmerged are the merged folders, by role, this account is left out of (ADR 80).
	Unmerged []string
}

// Merges says whether the account's folder for a merged folder feeds it.
func (s *Settings) Merges(role string) bool { return !slices.Contains(s.Unmerged, role) }

// ChooseMerged takes the ticked roles of those the form showed; a role the form did not show keeps its choice.
func ChooseMerged(ctx context.Context, l *alborz.Login, shown, ticked []string) error {
	return UpdateSettings(ctx, l, func(s *Settings) error {
		s.Unmerged = alborz.Untick(s.Unmerged, shown, ticked)
		return nil
	})
}

func LoadSettings(ctx context.Context, s alborz.Store) (*Settings, error) {
	settings := &Settings{}
	if err := s.Get(ctx, settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	return settings, nil
}

// UpdateSettings has edit change the account's settings as stored, taking turns with every other change.
func UpdateSettings(ctx context.Context, l *alborz.Login, edit func(*Settings) error) error {
	s := &Settings{}
	return l.Update(ctx, settingsKey, s, func() error { return edit(s) })
}

// Check names the limit the settings break, as a locale key and the limit, or "".
func (s *Settings) Check() (string, int) {
	switch {
	case len(s.Signatures) > MaxSignatures:
		return "form.signaturecount", MaxSignatures
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

// SignatureNamed is the signature of that name.
func (s *Settings) SignatureNamed(name string) (Signature, bool) {
	for _, sig := range s.Signatures {
		if sig.Name == name {
			return sig, true
		}
	}
	return Signature{}, false
}
