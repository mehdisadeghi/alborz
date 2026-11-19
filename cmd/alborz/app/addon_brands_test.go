//go:build !nobrands && !noaddons

package app_test

import (
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/internal/locale"
)

// The book's check reaches the message page: a brand's name at another domain beside a money
// subject is an alarm, with both facts, in every language.
func TestABorrowedBrandNameIsAWarningSign(t *testing.T) {
	a := Alborz(t)
	uid := a.IMAP.Append(t, "INBOX", "From: Aws Web Service <support@motulkzn.aynimarketing.co.za>\r\nTo: <"+User+">\r\n"+
		"Subject: Action required - Your AWS account is overdue for payment\r\n\r\nPay now.\r\n")
	c := a.SignIn(t, User)
	for _, lang := range locale.Languages {
		page := c.Page(t, "mail.message", "INBOX", uid, url.Values{"lang": {lang}})
		card := Between(page, `id="facts"`, "</details>")
		want := TIn(t, lang, "brands.elsewhere", `<bdi class="ltr">AWS</bdi>`, `<bdi class="ltr">aynimarketing.co.za</bdi>`)
		if !strings.Contains(page, "warning-alarm") || !strings.Contains(card, want) || !strings.Contains(card, TIn(t, lang, "indicator.moneysubject")) {
			t.Errorf("%s: the card reads %q, want an alarm saying %q and the money subject", lang, card, want)
		}
	}
	// Kept as not junk, the message is the reader's to judge.
	kept := a.IMAP.Append(t, "INBOX", "From: PayPal <service@example.net>\r\nTo: <"+User+">\r\nSubject: Hello\r\n\r\nHello.\r\n", "$NotJunk")
	if page := c.Page(t, "mail.message", "INBOX", kept); strings.Contains(page, `id="facts"`) {
		t.Error("a message kept as not junk shows warning signs")
	}
}
