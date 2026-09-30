package web_test

import (
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// Ticks, links and rows name a collection by its path, and an account's settings may be shared by
// every alborz it signs in to; each alborz's "Personal" is its own (ADR 28).
func TestTwoAlborzKeepTheirCollectionsApart(t *testing.T) {
	imap := IMAP(t)
	var held []string
	for _, host := range []string{"a.example", "b.example"} {
		c := Alborz(t, WithIMAP(imap), PublicURL("https://"+host)).SignIn(t, User)
		path := "/@" + host + c.CreateCalendar(t, "Personal", "")
		if !c.Listed(t)[path] {
			t.Fatalf("%s does not name its Personal by %s", host, path)
		}
		held = append(held, path)
	}
	if held[0] == held[1] {
		t.Errorf("both name their Personal %s", held[0])
	}
}

// A domain's DAV server that is another alborz is a server like any, not this one (ADR 28).
func TestADomainServedByAnotherAlborzListsItsCollections(t *testing.T) {
	imap := IMAP(t)
	other := Alborz(t, WithIMAP(imap))
	remote := At(other.URL) + other.SignIn(t, User).CreateCalendar(t, "Remote", "")
	c := Alborz(t, WithIMAP(imap), other.DAVUpstream()).SignIn(t, User)
	local := Here + c.CreateCalendar(t, "Local", "")
	if got := c.Listed(t); !got[remote] || !got[local] || len(got) != 2 {
		t.Errorf("listed %v, want the other alborz's Remote and this one's Local", got)
	}
}

// A domain whose DAV server is this alborz is this alborz, not a second server beside it.
func TestADomainServedByThisAlborzListsItsCollectionsOnce(t *testing.T) {
	c := Alborz(t, WithDAVHere(), NoCache()).SignIn(t, User)
	local := Here + c.CreateCalendar(t, "Local", "")
	if got := c.Listed(t); !got[local] || len(got) != 1 {
		t.Errorf("listed %v, want only this alborz's Local", got)
	}
}
