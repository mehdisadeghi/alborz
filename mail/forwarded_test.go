package mail

import (
	"bufio"
	"strings"
	"testing"

	"github.com/emersion/go-message/textproto"
)

// A real forward, trimmed; the clause keeps its exact shape for the parser.
const forwardedHeader = `Return-Path: <msk1361+caf_=mehdi=mehdix.org@gmail.com>
Delivered-To: mehdi@mehdix.org
ARC-Authentication-Results: i=3;
	mail.mehdix.org;
	dkim=pass header.d=info.ing.de header.s=elaine header.b=Y4hq5Khr;
	spf=pass (mail.mehdix.org: domain of "msk1361+caf_=mehdi=mehdix.org@gmail.com" designates 209.85.160.180 as permitted sender) smtp.mailfrom="msk1361+caf_=mehdi=mehdix.org@gmail.com";
	dmarc=pass (policy=reject) header.from=info.ing.de
X-Forwarded-To: mehdi@mehdix.org
X-Forwarded-For: msk1361@gmail.com mehdi@mehdix.org
Delivered-To: msk1361@gmail.com
From: ING <noreply@info.ing.de>
To: msk1361@gmail.com
Subject: Neuigkeiten

`

func headerOf(t *testing.T, raw string) textproto.Header {
	t.Helper()
	h, err := textproto.ReadHeader(bufio.NewReader(strings.NewReader(
		strings.ReplaceAll(raw, "\n", "\r\n"))))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestForwardedBy(t *testing.T) {
	spoofed := strings.Replace(forwardedHeader,
		`spf=pass (mail.mehdix.org: domain of "msk1361+caf_=mehdi=mehdix.org@gmail.com" designates 209.85.160.180 as permitted sender) smtp.mailfrom="msk1361+caf_=mehdi=mehdix.org@gmail.com";`,
		`spf=fail (mail.mehdix.org: domain of "attacker@evil.example" does not designate 1.2.3.4 as permitted sender) smtp.mailfrom="attacker@evil.example";`, 1)
	direct := strings.Replace(forwardedHeader,
		`smtp.mailfrom="msk1361+caf_=mehdi=mehdix.org@gmail.com"`,
		`smtp.mailfrom="bounce@info.ing.de"`, 1)
	for _, tc := range []struct{ name, raw, authserv, list, want string }{
		{"a forward our server vouched for", forwardedHeader, "mail.mehdix.org", "", "msk1361@gmail.com"},
		{"no trusted authserv", forwardedHeader, "", "", ""},
		{"somebody else's authserv", forwardedHeader, "mx.google.com", "", ""},
		{"list mail", forwardedHeader, "mail.mehdix.org", "tuhs.tuhs.org", ""},
		{"an SPF failure", spoofed, "mail.mehdix.org", "", ""},
		{"aligned mail", direct, "mail.mehdix.org", "", ""},
	} {
		if got := ForwardedBy(headerOf(t, tc.raw), tc.authserv, tc.list); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
