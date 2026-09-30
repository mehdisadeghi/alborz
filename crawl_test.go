package alborz_test

import (
	"html"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

var crawledLink = regexp.MustCompile(`(?:href|action|src|hx-get)="(/[^"]*)"`)

// html/template writes #ZgotmplZ for a URL it refuses, a link that goes nowhere and says nothing;
// only drawing every page finds one. Each page shape, a route and its parameters' names, is drawn once.
func TestNoPageDrawsARefusedLink(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	calendar, tasks, book := c.CreateCalendar(t, "Plans", ""), c.CreateCalendar(t, "Chores", "tasks"), c.CreateBook(t, "People")
	a.Put(t, calendar+"crawled.ics", "text/calendar", Event("crawled", "Crawled event"))
	a.Put(t, tasks+"crawled.ics", "text/calendar", Todo("crawled", "Crawled task"))
	a.Put(t, book+"crawled.vcf", "text/vcard", "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:crawled\r\nFN:Crawled Person\r\nN:Person;Crawled;;;\r\nEND:VCARD\r\n")

	queue := []string{
		a.Path("mail.mailbox", "INBOX"),
		a.Path("core.search", url.Values{"query": {"crawled"}}),
		a.Path("core.search", url.Values{"query": {"text:redesign"}}),
		a.Path("mail.search", url.Values{"query": {"text:redesign"}}),
		a.Path("core.search", url.Values{"view": {"red"}}),
	}
	shapes := map[string]bool{}
	drawn := 0
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		u, err := url.Parse(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		route, _, _ := a.Server.Match(u)
		shape := route + "?" + strings.Join(slices.Sorted(maps.Keys(u.Query())), "&")
		if route == "" || shapes[shape] || route == "auth.logout" {
			continue
		}
		shapes[shape] = true
		req, err := http.NewRequest(http.MethodGet, a.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		page := c.Do(t, req)
		if page.StatusCode != http.StatusOK || !strings.HasPrefix(page.Header.Get("Content-Type"), "text/html") {
			continue
		}
		drawn++
		if strings.Contains(page.Text, "ZgotmplZ") {
			i := strings.Index(page.Text, "ZgotmplZ")
			t.Errorf("%s draws a refused link: %q", path, page.Text[max(0, i-200):i+8])
		}
		for _, m := range crawledLink.FindAllStringSubmatch(page.Text, -1) {
			queue = append(queue, html.UnescapeString(m[1]))
		}
	}
	// A crawl that stopped at the first page would pass by drawing nothing.
	if drawn < 40 {
		t.Errorf("the crawl drew %d pages, too few to have walked the sections", drawn)
	}
	t.Logf("drew %d page shapes", drawn)
}
