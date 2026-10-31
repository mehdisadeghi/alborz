package web

import (
	"strings"
	"testing"

	"git.mehdix.org/alborz/mail"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestImageURLNormalization(t *testing.T) {
	msg := &mail.IMAPMessage{FetchMessageBuffer: &imapclient.FetchMessageBuffer{
		BodyStructure: &imap.BodyStructureSinglePart{Type: "image", Subtype: "png", ID: "<logo@example.org>"},
	}}
	san := sanitizer{msg: msg, raw: "/raw", proxy: "/proxy", allowRemoteResources: true}
	if got := san.sanitizeImageURL("cid:logo%40example.org"); got != "/raw?part=1" {
		t.Errorf("encoded CID: %q", got)
	}
	if got := san.sanitizeImageURL("http://images.example.org/a.png"); got != "/proxy?src=https%3A%2F%2Fimages.example.org%2Fa.png" {
		t.Errorf("legacy image: %q", got)
	}
	if got := san.sanitizeImageURL("//images.example.org/a.png"); got != "/proxy?src=https%3A%2F%2Fimages.example.org%2Fa.png" {
		t.Errorf("protocol-relative image: %q", got)
	}
}

func TestCSSImagesCannotUseUnresolvedURLFunctions(t *testing.T) {
	for _, value := range []string{`image-set("https://tracker.invalid/x" 1x)`, `-webkit-image-set("/private" 1x)`, `image("https://tracker.invalid/x")`, `u\72l(https://tracker.invalid/x)`, `URL(https://tracker.invalid/x`} {
		san := sanitizer{proxy: "/proxy", allowRemoteResources: true}
		body, err := san.sanitizeHTML([]byte(`<div style='background-image:` + value + `'>x</div>`))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "tracker.invalid") || strings.Contains(string(body), "/private") {
			t.Errorf("unresolved image syntax survived: %s", body)
		}
	}
}

func TestCSSImagesUseConsentAndTheProxy(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		san := sanitizer{proxy: "/proxy", allowRemoteResources: allowed}
		body, err := san.sanitizeHTML([]byte(`<style>p {background:URL("https://images.example.org/a(b).png")}</style><div style="background-image:url('https://images.example.org/b.png')">x</div>`))
		if err != nil {
			t.Fatal(err)
		}
		if !san.hasRemoteResources || strings.Contains(string(body), "https://images.example.org") {
			t.Errorf("CSS image bypasses consent/proxy: %s", body)
		}
		if got := strings.Count(string(body), "/proxy?src="); (got == 2) != allowed {
			t.Errorf("allowed=%v: %d proxied CSS images: %s", allowed, got, body)
		}
	}
}
