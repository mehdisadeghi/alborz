package alborzbase

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
	h := headerOf(t, forwardedHeader)

	if got := ForwardedBy(h, "mail.mehdix.org", ""); got != "msk1361@gmail.com" {
		t.Fatalf("got %q, want msk1361@gmail.com", got)
	}

	if got := ForwardedBy(h, "", ""); got != "" {
		t.Errorf("with no trusted authserv: got %q", got)
	}
	if got := ForwardedBy(h, "mx.google.com", ""); got != "" {
		t.Errorf("with somebody else's authserv: got %q", got)
	}
	if got := ForwardedBy(h, "mail.mehdix.org", "tuhs.tuhs.org"); got != "" {
		t.Errorf("on list mail: got %q", got)
	}
}

func TestForwardedBySpoofed(t *testing.T) {
	raw := strings.Replace(forwardedHeader,
		`spf=pass (mail.mehdix.org: domain of "msk1361+caf_=mehdi=mehdix.org@gmail.com" designates 209.85.160.180 as permitted sender) smtp.mailfrom="msk1361+caf_=mehdi=mehdix.org@gmail.com";`,
		`spf=fail (mail.mehdix.org: domain of "attacker@evil.example" does not designate 1.2.3.4 as permitted sender) smtp.mailfrom="attacker@evil.example";`, 1)
	if got := ForwardedBy(headerOf(t, raw), "mail.mehdix.org", ""); got != "" {
		t.Fatalf("an SPF failure was reported as a forward: %q", got)
	}

	direct := strings.Replace(forwardedHeader,
		`smtp.mailfrom="msk1361+caf_=mehdi=mehdix.org@gmail.com"`,
		`smtp.mailfrom="bounce@info.ing.de"`, 1)
	if got := ForwardedBy(headerOf(t, direct), "mail.mehdix.org", ""); got != "" {
		t.Fatalf("aligned mail was called a forward: %q", got)
	}
}
