package web

import (
	"encoding/base64"
	"errors"
	"strings"

	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
)

// SignatureChoice keeps identically named signatures distinct across accounts.
type SignatureChoice struct {
	mail.Signature
	Value, Account string
}

func signatureValue(account, name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(account + "\x00" + name))
}

func composeSignatures(ctx *web.Context, settings *mail.Settings) ([]SignatureChoice, error) {
	account, _ := mail.SplitIdentityChoice(ctx.FormValue("from_account"))
	if account == "" {
		account = ctx.Login.Username()
	}
	var choices []SignatureChoice
	add := func(owner string, settings *mail.Settings) {
		for _, sig := range settings.Signatures {
			value := sig.Name
			if ctx.Profile().Writing.PoolSignatures {
				value = signatureValue(owner, sig.Name)
			}
			choices = append(choices, SignatureChoice{Signature: sig, Value: value, Account: owner})
		}
	}
	if !ctx.Profile().Writing.PoolSignatures {
		add(account, settings)
		return choices, nil
	}
	for _, login := range ctx.Logins() {
		stored, err := mail.LoadSettings(ctx.Request().Context(), login.Store())
		if err != nil {
			return nil, err
		}
		add(login.Username(), stored)
	}
	return choices, nil
}

var errSignatureUnavailable = errors.New("signature is no longer available")

func chosenSignature(ctx *web.Context, settings *mail.Settings, chosen string) (mail.Signature, error) {
	if chosen == "" {
		return mail.Signature{}, nil
	}
	choices, err := composeSignatures(ctx, settings)
	if err != nil {
		return mail.Signature{}, err
	}
	for _, sig := range choices {
		if sig.Value == chosen {
			return sig.Signature, nil
		}
	}
	return mail.Signature{}, errSignatureUnavailable
}

const attribution = "Sent by Alborz Webmail"

// attributed leaves the editable body alone, including when SMTP refuses a send.
// Keeping this at the compose boundary excludes drafts and generated invitations.
func attributed(original *mail.OutgoingMessage) *mail.OutgoingMessage {
	msg := *original
	msg.Text = strings.TrimRight(msg.Text, "\r\n") + "\n\n" + attribution
	if msg.HTML != "" {
		msg.HTML += `<p dir="ltr">` + attribution + `</p>`
	}
	return &msg
}
