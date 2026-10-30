package web

import (
	"encoding/base64"
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

// chosenSignature is false for a signature no longer offered.
func chosenSignature(choices []SignatureChoice, chosen string) (mail.Signature, bool) {
	for _, sig := range choices {
		if sig.Value == chosen {
			return sig.Signature, true
		}
	}
	return mail.Signature{}, chosen == ""
}

// signOff is what goes under "-- ": the signature, then the attribution line unless the profile drops it.
func signOff(ctx *web.Context, sig string) string {
	if ctx.Profile().Writing.NoAttribution {
		return sig
	}
	return strings.TrimPrefix(sig+"\n"+ctx.T("compose.attribution"), "\n")
}

// signOffFormat is how a sign-off stands in one body format.
type signOffFormat struct {
	delimiter string
	block     func(signOff string) string
	locate    func(body, block string) (int, int, bool)
	add       func(body, block string, top bool) string
}

var plainSignOff = signOffFormat{
	delimiter: "\n-- \n",
	block:     func(s string) string { return "-- \n" + s },
	// Whole lines only, so a quoted "> -- " never matches.
	locate: func(body, block string) (int, int, bool) {
		padded, lines := "\n"+body+"\n", "\n"+block+"\n"
		if strings.Count(padded, lines) != 1 {
			return 0, 0, false
		}
		i := strings.Index(padded, lines)
		return i, i + len(block), true
	},
	add: func(body, block string, top bool) string {
		if top {
			return "\n\n" + block + "\n" + body
		}
		return body + "\n\n\n" + block
	},
}

var htmlSignOff = signOffFormat{
	delimiter: "-- <br>",
	block:     func(s string) string { return strings.TrimSpace(mail.WithSignatureHTML("", s)) },
	// The paragraph's own attributes may change on the way back from the editor; its content may not.
	locate: func(body, block string) (int, int, bool) {
		content := block[strings.Index(block, ">")+1:]
		if strings.Count(body, content) != 1 {
			return 0, 0, false
		}
		end := strings.Index(body, content)
		start := strings.LastIndex(body[:end], "<p")
		if start < 0 || strings.Index(body[start:end], ">") != end-start-1 {
			return 0, 0, false
		}
		return start, end + len(content), true
	},
	add: func(body, block string, top bool) string {
		if top {
			return `<p dir="auto"><br></p>` + block + "\n" + body
		}
		return body + "\n" + block
	},
}

// swap puts next in place of the sign-off one of offered left, standing exactly and once.
// Under no delimiter next is added, on top above a quote; an edited sign-off is left alone.
func (f signOffFormat) swap(body string, offered []string, next string, top bool) (string, bool) {
	at, end := -1, 0
	for _, s := range offered {
		if i, j, ok := f.locate(body, f.block(s)); ok {
			if at >= 0 {
				return body, false
			}
			at, end = i, j
		}
	}
	switch {
	case at >= 0 && next == "":
		return body[:at] + body[end:], true
	case at >= 0:
		return body[:at] + f.block(next) + body[end:], true
	case strings.Contains("\n"+body, f.delimiter):
		return body, false
	case next == "":
		return body, true
	}
	return f.add(body, f.block(next), top), true
}

// switchSignOff puts sig's sign-off in msg; false when the one there was edited and is kept.
func switchSignOff(ctx *web.Context, msg *mail.OutgoingMessage, choices []SignatureChoice, sig string, top bool) bool {
	var offered []string
	for _, s := range append(choices, SignatureChoice{}) {
		if block := signOff(ctx, s.Text); block != "" {
			offered = append(offered, block)
		}
	}
	next, ok := signOff(ctx, sig), false
	if msg.HTML != "" {
		if msg.HTML, ok = htmlSignOff.swap(msg.HTML, offered, next, top); ok {
			msg.Text = mail.ComposedText(msg.HTML)
		}
		return ok
	}
	msg.Text, ok = plainSignOff.swap(strings.ReplaceAll(msg.Text, "\r\n", "\n"), offered, next, top)
	return ok
}
