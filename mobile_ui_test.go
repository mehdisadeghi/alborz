package alborz_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/internal/locale"
	"golang.org/x/net/html"
)

func mobileClass(node *html.Node, class string) []*html.Node {
	var found []*html.Node
	for _, value := range strings.Fields(swipeAttribute(node, "class")) {
		if value == class {
			found = append(found, node)
			break
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		found = append(found, mobileClass(child, class)...)
	}
	return found
}

func TestPhoneNavigationKeepsSectionsScopedAndCurrent(t *testing.T) {
	a, c := SignedIn(t, User)
	c.SignIn(t, User2)
	if doc := swipeDocument(t, c.Page(t, "mail.mailbox", "INBOX")); len(mobileClass(doc, "phone-bar-shell")) != 0 {
		t.Fatal("phone navigation must remain opt-in")
	}
	if r := c.Post(t, "settings.profile", url.Values{"phone_bar": {"1"}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	sections := []struct {
		route, label string
	}{
		{"contacts.list", "nav.contacts"},
		{"calendar.journal", "nav.journal"},
		{"filters.list", "nav.filters"},
	}
	for _, section := range sections {
		t.Run(section.route, func(t *testing.T) {
			doc := swipeDocument(t, c.Page(t, section.route, url.Values{"account": {User2}}))
			bars := mobileClass(doc, "phone-bar")
			if len(bars) != 1 {
				t.Fatalf("got %d phone bars", len(bars))
			}
			bar := bars[0]
			more := mobileClass(bar, "phone-bar-more")
			if len(more) != 1 || more[0].Data != "button" || !strings.Contains(swipeAttribute(more[0], "aria-label"), locale.T("en", section.label)) {
				t.Fatal("More does not name the selected child section")
			}
			if len(mobileClass(more[0], "active")) != 1 {
				t.Fatal("More lacks its selected indicator")
			}
			popups := swipeElements(doc, "id", swipeAttribute(more[0], "popovertarget"))
			if len(popups) != 1 {
				t.Fatal("More does not address one unique popover")
			}
			current := swipeElements(popups[0], "aria-current", "page")
			if len(current) != 1 || swipeAttribute(current[0], "href") != a.Path(section.route, url.Values{"account": {User2}}) {
				t.Fatal("popover does not identify the current scoped destination")
			}
			for _, link := range swipeElements(bar, "href", "") {
				t.Fatalf("empty phone destination: %v", link)
			}
			for _, target := range []struct {
				route string
				args  []any
			}{
				{"mail.mailbox", []any{"INBOX"}}, {"calendar.month", nil}, {"contacts.list", nil},
				{"calendar.tasks", nil}, {"calendar.journal", nil}, {"filters.list", nil},
			} {
				args := append(target.args, url.Values{"account": {User2}})
				if len(swipeElements(bar, "href", a.Path(target.route, args...))) == 0 {
					t.Errorf("missing scoped destination %s", target.route)
				}
			}
		})
	}
}
