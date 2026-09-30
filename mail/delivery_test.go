package mail

import (
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// List mail delivered straight to the account, offering only a mailto to leave.
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
	q := unsubscribeMail(&Settings{}, trust, msg)
	if q == nil {
		t.Fatalf("a mailto unsubscribe did not become a message to compose")
	}
	for field, want := range map[string]string{"account": "mehdi@mehdix.org", "to": "~sircmpwn/sr.ht-discuss+unsubscribe@lists.sr.ht", "subject": "unsubscribe"} {
		if got := q.Get(field); got != want {
			t.Errorf("%s is %q, want %q", field, got, want)
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
	// A named server that wrote no Received here did not deliver this message.
	if _, cut := deliveryAddresses(h, "mail.example.org"); cut != 0 {
		t.Errorf("headers above a stranger's Received were believed: cut %d", cut)
	}
}

const aliasHeader = `Return-Path: <bounce@example.net>
Delivered-To: mx@43.yt
Received: from relay.example.net ([192.0.2.1])
	by ms4.migadu.com with LMTPS id abc
	for <mx@43.yt>; Thu, 03 Sep 2026 10:02:12 +0200
X-Envelope-To: immo43@43.yt
From: "Sender" <news@example.net>
To: <immo43@43.yt>
Subject: An offer

`

func TestAliasIsNotRepeatedWhenToNamesIt(t *testing.T) {
	msg := &IMAPMessage{FetchMessageBuffer: &imapclient.FetchMessageBuffer{Envelope: &imap.Envelope{
		To: []imap.Address{{Mailbox: "immo43", Host: "43.yt"}},
	}}}
	msg.setListHeaders(headerOf(t, aliasHeader))
	trust := deliveryTrust{account: "mx@43.yt", domains: []string{"43.yt"}}
	alias := trust.alias(msg)
	if alias != "immo43@43.yt" {
		t.Fatalf("the alias the mail reached is %q", alias)
	}
	if !addressed(alias, msg.Envelope.To, msg.Envelope.Cc) {
		t.Errorf("the alias is in To and the page would say it twice")
	}
	if addressed(alias, []imap.Address{{Mailbox: "list", Host: "example.org"}}) {
		t.Errorf("an alias To does not name is worth a row")
	}
}
