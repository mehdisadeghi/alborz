package mail

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestVouchedSender(t *testing.T) {
	one := []imap.Address{{Mailbox: "Ann", Host: "Example.org"}}
	for _, tc := range []struct {
		name    string
		from    []imap.Address
		results *AuthResults
		want    string
	}{
		{"dmarc pass", one, &AuthResults{DMARC: "pass"}, "ann@example.org"},
		{"aligned dkim", one, &AuthResults{DKIM: "pass", DKIMDomain: "example.org"}, "ann@example.org"},
		{"unaligned dkim", one, &AuthResults{DKIM: "pass", DKIMDomain: "other.net"}, ""},
		{"no verdict", one, nil, ""},
		{"two senders", append(one, imap.Address{Mailbox: "bo", Host: "example.org"}), &AuthResults{DMARC: "pass"}, ""},
		{"no address", nil, &AuthResults{DMARC: "pass"}, ""},
	} {
		got, ok := vouchedSender(tc.from, tc.results)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: got %q %v, want %q", tc.name, got, ok, tc.want)
		}
	}
}
