//go:build noaddons

package app_test

import (
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestCoreBuildOmitsAddonFeatures(t *testing.T) {
	a := Alborz(t)
	if len(a.Server.Addons) != 0 {
		t.Fatalf("core build offers %d add-ons", len(a.Server.Addons))
	}
	for _, segment := range a.Server.Status {
		if segment.Name == "moon" || segment.Name == "motd" || segment.Name == "weather" {
			t.Errorf("core build offers %s", segment.Name)
		}
	}
	for _, route := range a.Server.Routes() {
		for _, prefix := range []string{"moon.", "motd.", "season.", "weather."} {
			if strings.HasPrefix(route.Name, prefix) {
				t.Errorf("core build mounted %s", route.Name)
			}
		}
	}
	// No book of brands: a borrowed name is no warning sign.
	uid := a.IMAP.Append(t, "INBOX", "From: Aws Web Service <support@motulkzn.aynimarketing.co.za>\r\nTo: <"+User+">\r\n"+
		"Subject: Action required - Your AWS account is overdue for payment\r\n\r\nPay now.\r\n")
	if page := a.SignIn(t, User).Page(t, "mail.message", "INBOX", uid); !strings.Contains(page, "Pay now.") || strings.Contains(page, `id="facts"`) {
		t.Error("core build shows warning signs for a brand name, or not the message")
	}
	for _, theme := range a.Server.Themes() {
		for _, seasonal := range []string{"spring", "summer", "autumn", "winter"} {
			if theme.Name == seasonal {
				t.Errorf("core build offers %s theme", seasonal)
			}
		}
	}
}
