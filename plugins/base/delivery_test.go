package alborzbase

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// The headers of list mail delivered straight to the account, not to an
// alias: the list only offers a mailto to leave, and the message names
// the list, not the reader, in To.
const listHeader = `Return-Path: <lists@sr.ht>
Delivered-To: mehdi@mehdix.org
Received: from mail-a.sr.ht (mail-a.sr.ht [46.23.81.152])
	by mail.mehdix.org (Postfix) with ESMTPS id 9648065F
	for <mehdi@mehdix.org>; Thu, 03 Sep 2026 15:01:24 +0200 (CEST)
List-Unsubscribe: <mailto:~sircmpwn/sr.ht-discuss+unsubscribe@lists.sr.ht?subject=unsubscribe>
List-Post: <mailto:~sircmpwn/sr.ht-discuss@lists.sr.ht>
List-ID: ~sircmpwn/sr.ht-discuss <~sircmpwn/sr.ht-discuss.lists.sr.ht>
From: "Drew DeVault" <drew@ddevault.org>
To: "~sircmpwn/sr Ht-discuss" <~sircmpwn/sr.ht-discuss@lists.sr.ht>
Subject: Re: Changes to SourceHut's terms of service regarding LLMs

`

func listMessage(t *testing.T) *IMAPMessage {
	t.Helper()
	msg := &IMAPMessage{FetchMessageBuffer: &imapclient.FetchMessageBuffer{Envelope: &imap.Envelope{
		To: []imap.Address{{Mailbox: "~sircmpwn/sr.ht-discuss", Host: "lists.sr.ht"}},
	}}}
	msg.setListHeaders(headerOf(t, listHeader))
	return msg
}

func TestUnsubscribeComposesFromTheReceivingAccount(t *testing.T) {
	msg := listMessage(t)
	trust := deliveryTrust{account: "mehdi@mehdix.org", domains: []string{"mehdix.org"}}
	href := unsubscribeHref(&Settings{}, trust, msg)
	if !strings.HasPrefix(href, "/compose?") {
		t.Fatalf("a mailto unsubscribe did not become a compose link: %q", href)
	}
	for _, want := range []string{"account=mehdi@mehdix.org", "to=~sircmpwn%2Fsr.ht-discuss%2Bunsubscribe@lists.sr.ht", "subject=unsubscribe"} {
		if !strings.Contains(href, want) {
			t.Errorf("%q lacks %s", href, want)
		}
	}
}

func TestDeliveryHeadersAreBelievedOnlyBehindOurOwnReceived(t *testing.T) {
	h := headerOf(t, listHeader)
	if addrs, cut := deliveryAddresses(h, "mail.mehdix.org"); cut != len(addrs) || len(addrs) == 0 {
		t.Errorf("our own server's headers were not believed: %d of %v", cut, addrs)
	}
	if _, cut := deliveryAddresses(h, ""); cut == 0 {
		t.Errorf("with no server named there is nothing to measure against, so everything counts")
	}
	// A server the reader named as theirs that wrote no Received here
	// did not deliver this message; whatever sits on top is a stranger's.
	if _, cut := deliveryAddresses(h, "mail.example.org"); cut != 0 {
		t.Errorf("headers above a stranger's Received were believed: cut %d", cut)
	}
}
