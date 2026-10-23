package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestHiddenBooksStayOutOfCreatePickers(t *testing.T) {
	_, c := SignedIn(t, User)
	hidden := Here + c.CreateBook(t, "Hidden")
	c.CreateBook(t, "Visible")
	if resp := c.Post(t, "contacts.list", url.Values{"shown": {hidden}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("hide: %s", resp.Status)
	}
	picker := Between(c.Page(t, "contacts.contact.create"), `<select name="addressbook"`, `</select>`)
	if strings.Contains(picker, "Hidden") || !strings.Contains(picker, "Visible") {
		t.Fatalf("picker did not respect visibility: %s", picker)
	}
}
