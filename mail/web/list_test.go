package web_test

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/emersion/go-imap/v2"
	nethtml "golang.org/x/net/html"
)

// The login must still work after answering the bad query.
func TestUnknownSearchKeySurvives(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	if body := c.Page(t, "mail.mailbox", "INBOX", url.Values{"query": {"foo:bar"}}); body == "" {
		t.Fatal("the unrecognised key produced no page")
	}
	if len(MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))) == 0 {
		t.Fatal("the login stopped serving mail after an unrecognised search key")
	}
}

// The prefetch runs behind the listing, so the sequence repeats to meet a held body.
func TestOpeningAHeldMessageAfterAFlagChange(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	for range 3 {
		uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
		if len(uids) == 0 {
			t.Fatal("no messages listed")
		}
		time.Sleep(150 * time.Millisecond)
		next := a.Path("mail.message", "INBOX", uids[0], url.Values{"part": {"1"}})
		c.Post(t, "mail.messages.flag", url.Values{
			"uids": {uids[0]}, "flags": {`\Flagged`}, "action": {"add"}, "next": {next}}, "INBOX")
		if c.Follow(t, next) == "" {
			t.Fatal("the message page rendered empty after the flag change")
		}
	}
	if len(MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))) == 0 {
		t.Fatal("the login stopped serving mail after the flag change")
	}
}

func TestCachedMessageAndSidebarWithoutUpstream(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	path := a.Path("mail.message", "INBOX", uids[0], url.Values{"part": {"1"}})
	read := func(path, target string) (string, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, a.URL+path, nil)
		req.Header.Set("HX-Request", "true")
		req.Header.Set("HX-Target", target)
		resp := c.Do(t, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("response %d", resp.StatusCode)
		}
		return resp.Text, resp.Header.Get("Server-Timing")
	}
	warm := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		_, timing := read(path, "body")
		if strings.Contains(timing, "body_hit") {
			warm = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !warm {
		t.Fatal("message body never warmed")
	}
	a.IMAP.Close()
	c.HTTP.Timeout = time.Second
	if _, timing := read(path, "body"); !strings.Contains(timing, "body_hit") || strings.Contains(timing, "imap_work") {
		t.Fatalf("cached message used upstream: %s", timing)
	}
	read(a.Path("mail.sidebar", url.Values{"path": {a.Path("mail.message", "INBOX", uids[0])}}), "aside")
}

// A page that left its place to the script still steps for a browser that no longer runs it (ADR 13).
func TestDeferredPlaceStepsWithoutScript(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	base, _ := url.Parse(a.URL)
	c.HTTP.Jar.SetCookies(base, []*http.Cookie{{Name: "alborz_tz", Value: "Europe/Berlin", Path: "/"}})
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) < 3 {
		t.Fatalf("need three seeded messages, found %d", len(uids))
	}
	// A search no listing holds yet leaves the place to the page.
	page := c.Page(t, "mail.message", "INBOX", uids[1], url.Values{"query": {"from:example"}})
	next := regexp.MustCompile(`href="([^"]*step=next[^"]*)"`).FindStringSubmatch(page)
	if next == nil {
		t.Fatal("the deferred page offers no Next that works without script")
	}
	req, _ := http.NewRequest(http.MethodGet, a.URL+html.UnescapeString(next[1]), nil)
	resp := c.Do(t, req)
	to := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || strings.Contains(to, "step=") ||
		!strings.HasPrefix(to, strings.TrimSuffix(a.Path("mail.message", "INBOX", uids[1]), uids[1])) ||
		strings.HasPrefix(to, a.Path("mail.message", "INBOX", uids[1])+"?") {
		t.Fatalf("Next answered %s to %q", resp.Status, to)
	}
}

func TestStarStickToTheScopedAccount(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)

	scoped := url.Values{"account": {User2}}
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", scoped))
	if len(uids) == 0 {
		t.Fatal("the second account's inbox listed no message")
	}
	uid := uids[0]
	here := a.Path("mail.message", "INBOX", uid, url.Values{"part": {"1"}, "account": {User2}})

	if flagButtonColor(t, c.Follow(t, here)) != "" {
		t.Fatal("the message was already flagged before the test starred it")
	}
	c.Post(t, "mail.messages.flag", url.Values{"uids": {uid}, "color": {"red"}, "next": {here}}, "INBOX", scoped)

	if got := flagButtonColor(t, c.Follow(t, here)); got != "red" {
		t.Fatalf("after starring on the scoped page the star is %q, not gold", got)
	}
}

func flagButtonColor(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `class="flag-button`)
	if i < 0 {
		t.Fatal("the message page has no star button")
	}
	start := i + len(`class="`)
	end := strings.IndexByte(page[start:], '"')
	for cls := range strings.FieldsSeq(page[start : start+end]) {
		if strings.HasPrefix(cls, "flag-") && cls != "flag-button" {
			return strings.TrimPrefix(cls, "flag-")
		}
	}
	return ""
}

func TestNewestFirstWithoutSort(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"query": {"example"}}))
	if len(uids) < 2 {
		t.Fatalf("need two seeded messages to match, found %d", len(uids))
	}
	for i := 1; i < len(uids); i++ {
		if len(uids[i-1]) < len(uids[i]) || uids[i-1] < uids[i] {
			t.Fatalf("older message listed first: %v", uids)
		}
	}
}

func TestDatesPastTheSolarHijriReckoningShowGregorian(t *testing.T) {
	a := Alborz(t)
	dates := map[string]string{
		"Mon, 1 Jan 4000 10:00:00 +0000": "4000",
		"Fri, 1 Jan 0560 10:00:00 +0000": "560",
	}
	var uids []imap.UID
	for date := range dates {
		uids = append(uids, a.IMAP.Append(t, "INBOX", "From: a@example.org\r\nTo: "+User+
			"\r\nSubject: Dated\r\nDate: "+date+"\r\n\r\n"))
	}
	// Mail in place before the sign-in, whose first page lists the inbox: this is about dates, not arrival.
	c := a.SignIn(t, User)
	for _, reading := range []url.Values{
		{"calendar": {"shcal"}, "secondary": {""}},
		{"calendar": {"gregorian"}, "secondary": {"shcal"}},
	} {
		c.Post(t, "settings.profile", reading)
		list := c.Page(t, "mail.mailbox", "INBOX")
		for _, uid := range uids {
			c.Page(t, "mail.message", "INBOX", uid)
		}
		for _, shown := range dates {
			if !strings.Contains(list, shown) {
				t.Errorf("with %v the list does not show %q", reading, shown)
			}
		}
	}
}

func TestMessagesMoveDeleteAndFlag(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) < 3 {
		t.Fatalf("need three seeded messages, found %d", len(uids))
	}

	if resp := c.Post(t, "mail.messages.move", url.Values{"uids": {uids[0]}, "to": {"Archive"}}, "INBOX"); resp.StatusCode != http.StatusFound {
		t.Fatalf("move: %s", resp.Status)
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Archive"))); n != 1 {
		t.Errorf("Archive holds %d messages after a move, want 1", n)
	}

	if resp := c.Post(t, "mail.messages.delete", url.Values{"uids": {uids[1]}}, "INBOX"); resp.StatusCode != http.StatusFound {
		t.Fatalf("delete: %s", resp.Status)
	}
	left := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(left) != len(uids)-2 || slices.Contains(left, uids[1]) {
		t.Errorf("INBOX after a move and a delete: %v", left)
	}

	if resp := c.Post(t, "mail.messages.flag", url.Values{"uids": {uids[2]}, "flags": {"\\Flagged"}, "action": {"add"}}, "INBOX"); resp.StatusCode != http.StatusFound {
		t.Fatalf("flag: %s", resp.Status)
	}
	if starred := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"view": {"starred"}})); len(starred) != 1 || starred[0] != uids[2] {
		t.Errorf("starred view after flagging %s: %v", uids[2], starred)
	}
}

func TestStarViewKeepsItsQuery(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	refs := regexp.MustCompile(`name="refs" value="([^"]+)"`)
	var starred []string
	for _, query := range []string{"from:eve", "from:hal"} {
		uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"query": {query}}))
		if len(uids) != 1 {
			t.Fatalf("%q answers %d messages in INBOX, want 1", query, len(uids))
		}
		starred = append(starred, uids[0])
	}
	c.Post(t, "mail.messages.flag", url.Values{"uids": starred, "flags": {"\\Flagged"}, "action": {"add"}}, "INBOX")

	narrowed := a.Path("mail.search", url.Values{"view": {"starred"}, "query": {"from:eve"}})
	if rows := refs.FindAllString(c.Follow(t, narrowed), -1); len(rows) != 1 {
		t.Fatalf("starred and from:eve lists %d rows, want the one that is both", len(rows))
	}
	resp := c.Post(t, "mail.role.act", url.Values{"everything": {"1"}, "next": {narrowed}}, "INBOX", url.Values{"action": {"move"}, "to": {"Archive"}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("move: %s", resp.Status)
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Archive"))); n != 1 {
		t.Errorf("Archive holds %d after moving everything starred from eve, want 1", n)
	}
}

func TestFoldersAreMadeAndRemoved(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	if resp := c.Post(t, "mail.mailbox.create", url.Values{"name": {"Projects"}, "location": {"0"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("new folder: %s", resp.Status)
	}
	if page := c.Page(t, "mail.mailbox", "Projects"); !strings.Contains(page, "Projects") {
		t.Fatalf("the new folder is not listed")
	}
	if resp := c.Post(t, "mail.mailbox.delete", nil, "Projects"); resp.StatusCode != http.StatusFound {
		t.Fatalf("delete folder: %s", resp.Status)
	}
	if resp := c.Get(t, "mail.mailbox", "Projects"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a deleted folder still answers %s", resp.Status)
	}
}

// A subfolder's name holds the delimiter; its pager must keep it escaped.
func TestASubfolderPages(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) < 3 {
		t.Fatalf("need three seeded messages, found %d", len(uids))
	}
	if resp := c.Post(t, "mail.mailbox.create", url.Values{"name": {"Archive/2024"}, "location": {"0"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("new subfolder: %s", resp.Status)
	}
	c.Post(t, "mail.messages.move", url.Values{"uids": uids[:3], "to": {"Archive/2024"}}, "INBOX")
	body := c.Page(t, "mail.mailbox", "Archive/2024", url.Values{"ipp": {"1"}, "page": {"1"}})
	links := PagerLinks(body)
	if len(links) == 0 {
		t.Fatal("the middle page offers no step")
	}
	for _, link := range links {
		c.Follow(t, link)
	}
}

func TestNextStaysOnTheSite(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	message := a.Path("mail.message", "INBOX", uids[0])
	ours := a.Path("mail.mailbox", "INBOX", url.Values{"all": {"1"}})
	for next, want := range map[string]string{
		ours:                    ours,
		"https://evil.example/": message,
		"//evil.example/":       message,
		"/\\evil.example/":      message,
	} {
		resp := c.Post(t, "mail.messages.flag", url.Values{"uids": {uids[0]}, "flags": {"\\Seen"}, "next": {next}}, "INBOX")
		if to := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || to != want {
			t.Errorf("next=%q answered %s to %q, want %q", next, resp.Status, to, want)
		}
	}
}

// An undo with an undo would loop.
func TestMoveNoticeUndoes(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) == 0 {
		t.Fatal("the rig seeded no messages")
	}
	c.Post(t, "mail.messages.move", url.Values{"uids": {uids[0]}, "to": {"Archive"}}, "INBOX")
	action, fields := NoticeAction(c.Page(t, "mail.mailbox", "INBOX"))
	if fields.Get("undo") == "" {
		t.Fatalf("a move's notice offers no undo: %q %v", action, fields)
	}
	if resp := c.PostTo(t, action, fields); resp.StatusCode != http.StatusFound {
		t.Fatalf("undo: %s", resp.Status)
	}
	body := c.Page(t, "mail.mailbox", "INBOX")
	if n := len(MessageUIDs(body)); n != len(uids) {
		t.Errorf("INBOX holds %d after the undo, want %d", n, len(uids))
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Archive"))); n != 0 {
		t.Errorf("Archive still holds %d after the undo", n)
	}
	if action, _ := NoticeAction(body); action != "" {
		t.Errorf("the undo's notice offers %q", action)
	}
}

func TestSearchMoveUndoesIntoTheFolderItCameFrom(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	inbox, nested := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX")), MessageUIDs(c.Page(t, "mail.mailbox", "INBOX/Lists"))
	if len(nested) == 0 {
		t.Fatal("the rig seeded no messages in INBOX/Lists")
	}
	results := a.Path("mail.search", url.Values{"query": {"from:eve in:lists"}})
	c.Post(t, "mail.role.act", url.Values{"refs": {User + "|INBOX/Lists|" + nested[0]}, "next": {results}},
		"INBOX", url.Values{"action": {"move"}, "to": {"Archive"}})
	moved := c.Page(t, "mail.mailbox", "INBOX/Lists")
	if n := len(MessageUIDs(moved)); n != len(nested)-1 {
		t.Fatalf("INBOX/Lists holds %d after the move, want %d", n, len(nested)-1)
	}
	action, fields := NoticeAction(moved)
	if fields.Get("undo") == "" {
		t.Fatalf("the move's notice offers no undo: %q %v", action, fields)
	}
	c.PostTo(t, action, fields)
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "INBOX/Lists"))); n != len(nested) {
		t.Errorf("INBOX/Lists holds %d after the undo, want %d", n, len(nested))
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))); n != len(inbox) {
		t.Errorf("INBOX holds %d after the undo, want %d", n, len(inbox))
	}
}

// The first search is held on its first folder until the second reaches the server.
func TestANewerSearchWithdrawsTheOlder(t *testing.T) {
	rig := IMAP(t, Hold("stallword", "redesign"))
	a := Alborz(t, WithIMAP(rig))
	c := a.SignIn(t, User)
	older := make(chan *Response, 1)
	go func() { older <- c.Get(t, "mail.search", url.Values{"query": {"text:stallword"}}) }()
	<-rig.Held()
	newer := c.Page(t, "mail.search", url.Values{"query": {"redesign"}})
	if len(MessageUIDs(newer)) == 0 {
		t.Fatal("the newer search found nothing")
	}
	if resp := <-older; resp.StatusCode != http.StatusConflict {
		t.Errorf("the withdrawn search answered %s, want a conflict", resp.Status)
	}
	if n := rig.Asked(); n != 1 {
		t.Errorf("the withdrawn search asked %d folders, want the one it held", n)
	}
}

// Both offers are followed as the browser would, relative to the page that drew them.
func TestFolderSearchOffersEveryFolder(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	offer := func(page, from, label string) string {
		t.Helper()
		m := regexp.MustCompile(`<a[^>]*href="([^"]*)"[^>]*>` + regexp.QuoteMeta(html.EscapeString(label)) + `</a>`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("%s offers no %q", from, label)
		}
		base, err := url.Parse(from)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := url.Parse(html.UnescapeString(m[1]))
		if err != nil {
			t.Fatal(err)
		}
		return base.ResolveReference(ref).String()
	}
	folders := func(page string) []string {
		var out []string
		for _, ref := range rowRefs(page) {
			out = append(out, strings.Split(ref, "|")[1])
		}
		slices.Sort(out)
		return out
	}
	want := []string{"INBOX", "INBOX/Lists"}
	inbox := a.Path("mail.mailbox", "INBOX", url.Values{"query": {"eve"}})
	everywhere := offer(c.Follow(t, inbox), inbox, T(t, "search.allfolders"))
	results := c.Follow(t, everywhere)
	if got := folders(results); !slices.Equal(got, want) {
		t.Fatalf("all folders found eve in %v, want %v", got, want)
	}
	whole := c.Follow(t, offer(results, everywhere, T(t, "mailbox.searchtext")))
	if got := folders(whole); !slices.Equal(got, want) {
		t.Errorf("the whole message found eve in %v, want %v", got, want)
	}
}

func TestDroppedTwiceIsImportedOnce(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	eml := "From: ann@example.org\r\nTo: " + User + "\r\nSubject: dropped\r\n" +
		"Message-ID: <dropped@test>\r\nContent-Type: text/plain\r\n\r\nBody.\r\n"
	for range 2 {
		resp := c.Upload(t, "mail.mailbox.import", nil, []File{{Field: "file", Name: "dropped.eml", Body: strings.NewReader(eml)}}, "Archive")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("drop: %s", resp.Status)
		}
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Archive"))); n != 1 {
		t.Errorf("Archive holds %d after the same file dropped twice, want 1", n)
	}
}

func bothAccounts(t *testing.T, a *App) *Client {
	t.Helper()
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	return c
}

func rowRefs(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`name="refs" value="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

// The second page is where an off-by-one cut or a one-account total shows.
func TestMergedListsCutTheSamePage(t *testing.T) {
	a := Alborz(t)
	c := bothAccounts(t, a)
	for _, list := range [][]any{{"mail.mailbox", "INBOX"}, {"mail.search", url.Values{"query": {"example"}}}} {
		route, args := list[0].(string), list[1:]
		all := rowRefs(c.Page(t, route, append(args, url.Values{"ipp": {"100"}})...))
		if len(all) < 5 {
			t.Fatalf("%s: need five seeded rows to have a third page, found %d", route, len(all))
		}
		body := c.Page(t, route, append(args, url.Values{"ipp": {"2"}, "page": {"1"}})...)
		if got := rowRefs(body); !slices.Equal(got, all[2:4]) {
			t.Errorf("%s: the second page holds %v, want %v", route, got, all[2:4])
		}
	}
}

// A message opened from the merged inbox walks the merged rows, across
// accounts, reading past the page it was on for the row after its last.
func TestAMergedListIsWalkedInItsOwnOrder(t *testing.T) {
	a := Alborz(t)
	c := bothAccounts(t, a)
	all := rowRefs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"ipp": {"100"}}))
	if len(all) != 6 {
		t.Fatalf("the merged inbox lists %v, want both accounts' six messages", all)
	}
	step := func(page, key string) string {
		m := regexp.MustCompile(`<a [^>]*href="([^"]+)"[^>]*data-key="` + key + `"`).FindStringSubmatch(page)
		if m == nil {
			return ""
		}
		u, err := url.Parse(html.UnescapeString(m[1]))
		if err != nil {
			t.Fatalf("neighbour link %q: %v", m[1], err)
		}
		_, values, ok := a.Server.Match(u)
		if !ok {
			t.Fatalf("neighbour link %q is no route", m[1])
		}
		return u.Query().Get("account") + "|" + values["mbox"] + "|" + values["uid"]
	}
	// The last row of the second page, whose next is on the third, and a row inside one page.
	for _, at := range []struct {
		list     url.Values
		row, pos int
	}{{url.Values{"ipp": {"2"}, "page": {"1"}}, 1, 3}, {url.Values{"ipp": {"100"}}, 4, 4}} {
		links := subjectLinks(t, c.Page(t, "mail.mailbox", "INBOX", at.list))
		if len(links) <= at.row {
			t.Fatalf("%v lists %d rows", at.list, len(links))
		}
		msg := c.Follow(t, links[at.row])
		if prev, next := step(msg, "k"), step(msg, "j"); prev != all[at.pos-1] || next != all[at.pos+1] {
			t.Errorf("%v: row %d steps to %q and %q, want %q and %q", at.list, at.row, prev, next, all[at.pos-1], all[at.pos+1])
		}
	}
}

// A folder that is no role is merged from the accounts that hold it; one
// that no account holds is not there, rather than an empty list.
func TestAMergedFolderByNameListsWhoHoldsIt(t *testing.T) {
	c := bothAccounts(t, Alborz(t))
	refs := rowRefs(c.Page(t, "mail.mailbox", "INBOX/Lists"))
	if len(refs) != 5 {
		t.Fatalf("the merged INBOX/Lists lists %v, want the five seeded messages", refs)
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref, User+"|INBOX/Lists|") {
			t.Errorf("row %q is not from the account holding the folder's mail", ref)
		}
	}
	if resp := c.Get(t, "mail.mailbox", "Nowhere"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a folder no account holds answered %s, want 404", resp.Status)
	}
}

func TestAServerDownCostsItsRowsNotThePage(t *testing.T) {
	elsewhere := IMAP(t)
	a := Alborz(t, Upstream("elsewhere.local", elsewhere.Upstream()))
	c := a.SignIn(t, User)
	c.SignIn(t, Elsewhere)
	if rows := rowAccounts(c.Page(t, "mail.mailbox", "INBOX")); rows[User] == 0 || rows[Elsewhere] == 0 {
		t.Fatalf("the merged inbox is not both accounts: %v", rows)
	}

	elsewhere.Close()
	// The loss is noticed a moment after Close returns; until then the cache answers.
	var body string
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		c.Post(t, "mail.mailbox.refresh", nil, "INBOX")
		body = c.Page(t, "mail.mailbox", "INBOX")
		if Says(t, body, "notice.unreachable", 1, "Mail", Any) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the inbox: nothing says a server did not answer: %s", NoticeBar(body))
		}
	}
	if !strings.Contains(NoticeBar(body), Elsewhere) {
		t.Errorf("the inbox: the notice does not name the account: %s", NoticeBar(body))
	}
	if rows := rowAccounts(body); rows[User] == 0 || rows[Elsewhere] != 0 {
		t.Errorf("the inbox: rows by account are %v", rows)
	}
	// A warning is said once per while, so the next page does not repeat it; its rows still cost nothing.
	c.Post(t, "mail.mailbox.refresh", nil, "INBOX")
	body = c.Page(t, "mail.search", url.Values{"query": {"example"}})
	if Says(t, body, "notice.unreachable", 1, "Mail", Any) {
		t.Errorf("the search repeats the warning the inbox just said: %s", NoticeBar(body))
	}
	if rows := rowAccounts(body); rows[User] == 0 || rows[Elsewhere] != 0 {
		t.Errorf("the search: rows by account are %v", rows)
	}
}

func rowAccounts(body string) map[string]int {
	out := map[string]int{}
	for _, m := range regexp.MustCompile(`name="refs" value="([^|]+)\|`).FindAllStringSubmatch(body, -1) {
		out[m[1]]++
	}
	return out
}

func TestScopeSelectsAccounts(t *testing.T) {
	c := bothAccounts(t, Alborz(t))
	merged := rowAccounts(c.Page(t, "mail.mailbox", "INBOX"))
	if merged[User] == 0 || merged[User2] == 0 {
		t.Fatalf("the merged inbox is not both accounts: %v", merged)
	}
	for account, other := range map[string]string{User: User2, User2: User} {
		scoped := c.Page(t, "mail.mailbox", "INBOX", url.Values{"account": {account}})
		if n := len(MessageUIDs(scoped)); n == 0 {
			t.Errorf("%s has no rows when the page is scoped to it", account)
		}
		if strings.Contains(scoped, `value="`+other+`|`) {
			t.Errorf("a page scoped to %s shows %s's mail", account, other)
		}
	}
}

// A conversation list is threaded once for all its pages, and a message
// opened from it walks the conversations' order, not the folder's.
func TestAConversationListThreadsOnceAndItsMessagesWalkIt(t *testing.T) {
	a := Alborz(t, WithIMAP(IMAP(t, Sorting())))
	c := a.SignIn(t, User)
	var order, links []string
	for page := range 3 {
		body := c.Page(t, "mail.mailbox", "INBOX", url.Values{"sort": {"thread"}, "ipp": {"1"}, "page": {fmt.Sprint(page)}})
		order = append(order, rowUIDs(body)...)
		links = append(links, subjectLinks(t, body)...)
	}
	if len(order) != 5 || len(links) != 5 || slices.IsSortedFunc(order, func(a, b string) int { return strings.Compare(b, a) }) {
		t.Fatalf("three pages of one conversation each list %v, want the five seeded messages out of the folder's order", order)
	}
	msg := c.Follow(t, links[2])
	if prev, next := neighbourUID(t, msg, "k"), neighbourUID(t, msg, "j"); prev != order[1] || next != order[3] {
		t.Errorf("the third message steps to %q and %q, want %q and %q", prev, next, order[1], order[3])
	}
	if n := a.IMAP.Threads(); n != 1 {
		t.Errorf("three pages and a message sent %d THREADs, want 1", n)
	}
}

// neighbourUID is the UID a message page's k or j link leads to.
func neighbourUID(t *testing.T, page, key string) string {
	t.Helper()
	m := regexp.MustCompile(`<a [^>]*href="([^"]+)"[^>]*data-key="` + key + `"`).FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	u, err := url.Parse(html.UnescapeString(m[1]))
	if err != nil {
		t.Fatalf("neighbour link %q: %v", m[1], err)
	}
	return u.Path[strings.LastIndexByte(u.Path, '/')+1:]
}

// rowUIDs are the UIDs of a mail list's rows, in the order shown.
func rowUIDs(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`name="uids" value="(\d+)" form="messages-form"`).FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// The pages of a sorted list share one SORT, which only a change asks again.
func TestASortedListSortsOncePerChange(t *testing.T) {
	a := Alborz(t, WithIMAP(IMAP(t, Sorting())))
	c := a.SignIn(t, User)
	page := func(n int) []string {
		return rowUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"sort": {"subject"}, "dir": {"asc"}, "ipp": {"2"}, "page": {fmt.Sprint(n)}}))
	}
	var seen []string
	for n := range 3 {
		uids := page(n)
		if len(uids) == 0 {
			t.Fatalf("page %d of the sorted inbox lists no rows", n)
		}
		seen = append(seen, uids...)
	}
	if len(seen) != 5 || len(slices.Compact(slices.Sorted(slices.Values(seen)))) != 5 {
		t.Fatalf("three pages of two list %v, want the five seeded messages once each", seen)
	}
	if n := a.IMAP.Sorts(); n != 1 {
		t.Errorf("three pages of one order sent %d SORTs, want 1", n)
	}
	if resp := c.Post(t, "mail.messages.move", url.Values{"uids": {seen[0]}, "to": {"Archive"}}, "INBOX"); resp.StatusCode != http.StatusFound {
		t.Fatalf("move: %s", resp.Status)
	}
	if uids := page(1); slices.Contains(uids, seen[0]) || len(uids) == 0 {
		t.Errorf("the second page after a move lists %v", uids)
	}
	if n := a.IMAP.Sorts(); n != 2 {
		t.Errorf("a page after a move sent %d SORTs in all, want 2", n)
	}
}

// Read the first link within each subject cell, independent of its layout wrappers.
func subjectLinks(t *testing.T, page string) []string {
	t.Helper()
	doc, err := nethtml.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	attr := func(n *nethtml.Node, key string) string {
		for _, a := range n.Attr {
			if a.Key == key {
				return a.Val
			}
		}
		return ""
	}
	var firstLink func(*nethtml.Node) string
	firstLink = func(n *nethtml.Node) string {
		if n.Type == nethtml.ElementNode && n.Data == "a" {
			return attr(n, "href")
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if link := firstLink(ch); link != "" {
				return link
			}
		}
		return ""
	}
	var links []string
	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		if attr(n, "role") == "cell" && slices.Contains(strings.Fields(attr(n, "class")), "message-list-subject") {
			if link := firstLink(n); link != "" {
				links = append(links, link)
			}
			return
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)
	return links
}

// A message opened from the third page walks to the rows either side of it in
// that order, taken from the order the pages were cut from.
func TestNeighboursOnTheThirdPageOfASortedList(t *testing.T) {
	a := Alborz(t, WithIMAP(IMAP(t, Sorting())))
	c := a.SignIn(t, User)
	sorted := url.Values{"sort": {"subject"}, "dir": {"desc"}}
	order := rowUIDs(c.Page(t, "mail.mailbox", "INBOX", sorted, url.Values{"ipp": {"100"}}))
	if len(order) != 5 {
		t.Fatalf("the sorted inbox lists %v, want the five seeded messages", order)
	}
	page := c.Page(t, "mail.mailbox", "INBOX", sorted, url.Values{"ipp": {"1"}, "page": {"2"}})
	links := subjectLinks(t, page)
	if len(links) == 0 {
		t.Fatal("the third page has no row to open")
	}
	msg := c.Follow(t, links[0])
	if prev, next := neighbourUID(t, msg, "k"), neighbourUID(t, msg, "j"); prev != order[1] || next != order[3] {
		t.Errorf("the third message steps to %q and %q, want %q and %q", prev, next, order[1], order[3])
	}
	if n := a.IMAP.Sorts(); n != 1 {
		t.Errorf("listing and opening sent %d SORTs, want the one", n)
	}
}

// A message the server refuses is counted, and the ones after it are imported.
func TestADroppedMessageTheServerRefusesIsCounted(t *testing.T) {
	a := Alborz(t, WithIMAP(IMAP(t, RefuseAppend("Subject: refused"))))
	c := a.SignIn(t, User)
	message := func(subject string) string {
		return "From ann@example.org Mon Sep 28 10:00:00 2026\nFrom: ann@example.org\nTo: " + User + "\nSubject: " + subject +
			"\nMessage-ID: <" + subject + "@test>\n\nBody.\n\n"
	}
	mbox := message("first") + message("refused") + message("last")
	resp := c.Upload(t, "mail.mailbox.import", nil, []File{{Field: "file", Name: "three.mbox", Body: strings.NewReader(mbox)}}, "Archive")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("drop: %s", resp.Status)
	}
	page := c.Page(t, "mail.mailbox", "Archive")
	if n := len(MessageUIDs(page)); n != 2 {
		t.Errorf("Archive holds %d, want the 2 the server took", n)
	}
	if bar := NoticeBar(page); !Says(t, bar, "notice.importrefused", 1) {
		t.Errorf("the notice does not count the refused message:\n%s", bar)
	}
}

// An account left out of the merged inbox takes its rows, its share of the
// total and its unread count with it; its own inbox keeps them.
func TestAnAccountLeftOutOfTheMergedInbox(t *testing.T) {
	c := bothAccounts(t, Alborz(t))
	merged := func() map[string]int {
		body := c.Page(t, "mail.mailbox", "INBOX", url.Values{"ipp": {"100"}})
		rows := rowAccounts(body)
		if rows[User]+rows[User2] == 0 {
			t.Fatal("the merged inbox lists no row")
		}
		return rows
	}
	total := func(rows map[string]int) int {
		n := 0
		for _, k := range rows {
			n += k
		}
		return n
	}
	rows := merged()
	unseen := badge(t, c.Page(t, "mail.sidebar"))
	if rows[User] == 0 || rows[User2] == 0 || unseen == 0 {
		t.Fatalf("the merged inbox is not both accounts with unread mail: %v, %d unread", rows, unseen)
	}
	resp := c.Post(t, "mail.merged", url.Values{"shown": {"INBOX"}, "next": {"/mailbox/INBOX"}}, url.Values{"account": {User2}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("leaving the inbox out answered %s", resp.Status)
	}
	after := merged()
	if after[User] != rows[User] || after[User2] != 0 {
		t.Errorf("rows by account are %v, want %d of %s's and none of %s's", after, rows[User], User, User2)
	}
	if want := total(rows) - rows[User2]; total(after) != want {
		t.Errorf("the listing shows %d rows, was %d, want %d", total(after), total(rows), want)
	}
	if badge(t, c.Page(t, "mail.sidebar")) == unseen {
		t.Errorf("the rail still counts %d unread", unseen)
	}
	if own := rowUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"account": {User2}})); len(own) != rows[User2] {
		t.Errorf("%s's own inbox lists %d rows, want %d", User2, len(own), rows[User2])
	}
}

// Two quick ticks on different folders both stay left out, whichever is written first.
func TestTwoQuickMergedTicksBothHold(t *testing.T) {
	a := Alborz(t, WithIMAP(IMAP(t, Metadata())))
	c := bothAccounts(t, a)
	errs := make(chan error, 2)
	for _, role := range []string{"INBOX", "Drafts"} {
		go func() {
			form := url.Values{"shown": {role}, "next": {a.Path("mail.mailbox", "INBOX")}}
			resp, err := c.HTTP.PostForm(a.URL+a.Path("mail.merged", url.Values{"account": {User}}), form)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode != http.StatusFound {
					err = fmt.Errorf("leaving %s out answered %s", role, resp.Status)
				}
			}
			errs <- err
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	ticks := regexp.MustCompile(`name="merged" value="(\w+)"( checked)?`).FindAllStringSubmatch(Between(c.Page(t, "mail.mailbox", "INBOX", url.Values{"account": {User}}), `<form id="merged-`+User, "</form>"), -1)
	if len(ticks) == 0 {
		t.Fatal("the rail shows no merged-view ticks")
	}
	for _, m := range ticks {
		if left := m[1] == "INBOX" || m[1] == "Drafts"; left == (m[2] != "") {
			t.Errorf("%s is ticked %t, want %t", m[1], m[2] != "", !left)
		}
	}
}

// A swap into main leaves the rail in place; what on the page names where to come back to names the list the swap shows.
func TestASwapLeavesNoWayBackToTheListBefore(t *testing.T) {
	a := Alborz(t, WithIMAP(IMAP(t, Metadata())))
	c := bothAccounts(t, a)
	before := c.Page(t, "mail.mailbox", "INBOX")
	if !strings.Contains(before, `id="merged-next-`+User+`"`) {
		t.Fatal("the merged inbox has no merged-view ticks")
	}
	sent := a.Path("mail.mailbox", "Sent", url.Values{"view": {"unread"}})
	for _, next := range Returns(Swapped(before, c.Swap(t, sent).Text)) {
		if next != sent {
			t.Errorf("the page returns to %q, not to the list it shows, %q", next, sent)
		}
	}
}

// A signature added while the account form saves survives it, and so does the form's name.
func TestASignatureAndTheAccountFormSavedTogetherBothHold(t *testing.T) {
	a := Alborz(t, WithIMAP(IMAP(t, Metadata())))
	c := a.SignIn(t, User)
	saves := []struct {
		route string
		form  url.Values
	}{
		{"mail.account", url.Values{"from": {"Ada Lovelace"}}},
		{"mail.signatures", url.Values{"name": {"Work"}, "text": {"Regards"}}},
	}
	errs := make(chan error, len(saves))
	for _, s := range saves {
		go func() {
			resp, err := c.HTTP.PostForm(a.URL+a.Path(s.route), s.form)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode != http.StatusFound {
					err = fmt.Errorf("saving %s answered %s", s.route, resp.Status)
				}
			}
			errs <- err
		}()
	}
	for range saves {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	account, signatures := c.Page(t, "mail.account"), c.Page(t, "mail.signatures")
	if account == "" || signatures == "" {
		t.Fatal("the account or signatures page came back empty")
	}
	if !strings.Contains(account, `value="Ada Lovelace"`) {
		t.Error("the account form's name was lost")
	}
	if !strings.Contains(signatures, "Work") {
		t.Error("the signature was lost")
	}
}

// A tab drawn before another browser saved changes only what it changed, and the other browser's save stays.
func TestAStaleAccountFormKeepsAnotherBrowsersSave(t *testing.T) {
	a := Alborz(t, WithIMAP(IMAP(t, Metadata())))
	stale, other := a.SignIn(t, User), a.SignIn(t, User)
	drawn := Shown(stale.Page(t, "mail.account"))
	if resp := other.Post(t, "mail.account", url.Values{"shown": {Shown(other.Page(t, "mail.account"))}, "from": {"Ada Lovelace"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the name answered %s", resp.Status)
	}
	if resp := stale.Post(t, "mail.account", url.Values{"shown": {drawn}, "from": {""}, "trusted_authserv": {"mail.example.org"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the authserv-id answered %s", resp.Status)
	}
	page := other.Page(t, "mail.account")
	if !strings.Contains(page, `value="Ada Lovelace"`) {
		t.Error("the name saved in the other browser is gone")
	}
	if !strings.Contains(page, `value="mail.example.org"`) {
		t.Error("the stale tab's authserv-id is gone")
	}
}

// A signature renamed in a tab drawn before another browser changed its text keeps that text.
func TestAStaleSignatureEditKeepsAnotherBrowsersSave(t *testing.T) {
	a := Alborz(t, WithIMAP(IMAP(t, Metadata())))
	stale, other := a.SignIn(t, User), a.SignIn(t, User)
	if resp := other.Post(t, "mail.signatures", url.Values{"name": {"Work"}, "text": {"Regards"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("adding the signature answered %s", resp.Status)
	}
	drawn := Shown(stale.Page(t, "mail.signature", "Work"))
	shown := Shown(other.Page(t, "mail.signature", "Work"))
	if resp := other.Post(t, "mail.signatures", url.Values{"shown": {shown}, "was": {"Work"}, "name": {"Work"}, "text": {"Best wishes"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("changing the text answered %s", resp.Status)
	}
	if resp := stale.Post(t, "mail.signatures", url.Values{"shown": {drawn}, "was": {"Work"}, "name": {"Office"}, "text": {"Regards"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("renaming answered %s", resp.Status)
	}
	page := other.Page(t, "mail.signature", "Office")
	if !strings.Contains(page, "Best wishes") {
		t.Error("the text saved in the other browser is gone")
	}
}

func TestMailSearchRetainsSidebarOnNavigationAndHistory(t *testing.T) {
	a, c := SignedIn(t, User)
	for _, accounts := range []string{"single", "merged"} {
		if accounts == "merged" {
			c.SignIn(t, User2)
		}
		for _, query := range []url.Values{{"view": {"starred"}}, {"view": {"red"}}, {"query": {"hello"}}} {
			for _, navigation := range []string{"direct", "boosted", "history"} {
				t.Run(accounts+"/"+query.Encode()+"/"+navigation, func(t *testing.T) {
					req, err := http.NewRequest(http.MethodGet, a.URL+a.Path("mail.search", query), nil)
					if err != nil {
						t.Fatal(err)
					}
					switch navigation {
					case "boosted":
						req.Header.Set("HX-Request", "true")
						req.Header.Set("HX-Boosted", "true")
						req.Header.Set("HX-Target", "body")
					case "history":
						req.Header.Set("HX-History-Restore-Request", "true")
					}
					response := c.Do(t, req)
					if response.StatusCode != http.StatusOK {
						t.Fatalf("search: %s", response.Status)
					}
					if !strings.Contains(response.Text, "data-mail-sidebar=") {
						t.Fatal("search navigation lost the mail sidebar")
					}
				})
			}
		}
	}
}

func badge(t *testing.T, page string) int {
	t.Helper()
	_, rest, ok := strings.Cut(page, `data-unread="`)
	n, _, _ := strings.Cut(rest, `"`)
	count, err := strconv.Atoi(n)
	if !ok || err != nil {
		t.Fatalf("the rail carries no unread count: %q", n)
	}
	return count
}

// The rail carries the unread count of every signed-in account's inbox for the app's badge, and a
// refresh of it follows new mail; read mail does not count.
func TestTheRailCarriesTheUnreadCount(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	one := badge(t, c.Page(t, "mail.mailbox", "INBOX"))
	c.SignIn(t, User2)
	before := badge(t, c.Page(t, "mail.mailbox", "INBOX"))
	if one == 0 || before <= one {
		t.Fatalf("one account counts %d, two %d: the seeded inboxes hold unread mail", one, before)
	}
	a.IMAP.Append(t, "INBOX", "Subject: Unread\r\n\r\nHello\r\n")
	a.IMAP.Append(t, "INBOX", "Subject: Read\r\n\r\nHello\r\n", imap.FlagSeen)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		now := badge(t, c.Page(t, "mail.sidebar"))
		if now == before+1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the rail counts %d unread, want %d", now, before+1)
		}
	}
}

func TestTheUnreadSegmentCountsTheInbox(t *testing.T) {
	a := Alborz(t)
	status, err := a.IMAP.Dial(t).Status("INBOX", &imap.StatusOptions{NumUnseen: true}).Wait()
	if err != nil {
		t.Fatal(err)
	}
	unseen := int(*status.NumUnseen)
	if unseen == 0 {
		t.Fatal("the fixture's inbox holds no unread mail; the count proves nothing")
	}
	c := a.SignIn(t, User)
	count := func(q url.Values) int {
		t.Helper()
		page := c.Page(t, "core.status", "mail", q)
		n := 1
		if digits := regexp.MustCompile(`\d[\d,]*`).FindString(html.UnescapeString(page)); digits != "" {
			n, _ = strconv.Atoi(strings.ReplaceAll(digits, ",", ""))
		}
		if !Says(t, page, "mail.status.unread", n) {
			t.Fatalf("the segment does not count %d unread: %q", n, page)
		}
		return n
	}
	if got := count(nil); got != unseen {
		t.Errorf("one account: %d unread, the server says %d", got, unseen)
	}

	c.SignIn(t, User2)
	second := count(url.Values{"account": {User2}})
	if got := count(url.Values{"account": {User}}); got != unseen {
		t.Errorf("%s: %d unread, the server says %d", User, got, unseen)
	}
	if got := count(nil); got != unseen+second {
		t.Errorf("merged: %d unread, want %d and %d", got, unseen, second)
	}
}

// idle is the tests' login idle limit, long enough for a sign-in and a page.
const idle = 500 * time.Millisecond

func logins(a *App, user string) int {
	n := 0
	for _, l := range a.Server.Logins() {
		if l.Username() == user {
			n++
		}
	}
	return n
}

// awaitLogins waits for the account's live logins to come to want.
func awaitLogins(t *testing.T, a *App, want int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); logins(a, User) != want; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d logins of %s, want %d", logins(a, User), User, want)
		}
	}
}

// A browser with notifications on keeps its login past the idle limit, and new mail still reaches it.
func TestADeviceKeepsItsLoginForNotifications(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t, Guarded(push.Client().Transport), LoginIdle(idle))
	endpoint := push.TurnOn(t, a.SignIn(t, User))
	time.Sleep(3 * idle)
	if n := logins(a, User); n != 1 {
		t.Fatalf("%d logins after the idle limit, want the one kept", n)
	}
	arrive(t, a, push, endpoint)
}

// With no browser to notify, a login ends at the idle limit as it always has.
func TestWithoutADeviceALoginEndsWhenIdle(t *testing.T) {
	a := Alborz(t, LoginIdle(idle))
	a.SignIn(t, User)
	awaitLogins(t, a, 0)
}

// Turning the device off from another browser lets the kept login go at once, not at its next idle check:
// with a check a second apart, waiting for it would pass a quarter second only one time in four.
func TestTurningTheDeviceOffEndsTheKeptLogin(t *testing.T) {
	const idle = time.Second
	push := PushServer(t)
	a := Alborz(t, Guarded(push.Client().Transport), LoginIdle(idle))
	push.TurnOn(t, a.SignIn(t, User))
	other := a.Client()
	if resp := other.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}, "remember-me": {"on"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("signing in: %s", resp.Status)
	}
	time.Sleep(3 * idle)
	page := other.Page(t, "settings.notifications")
	awaitLogins(t, a, 2)
	id := regexp.MustCompile(`name="device" value="([^"]+)"`).FindStringSubmatch(page)
	if id == nil {
		t.Fatal("the other browser lists no device")
	}
	start := time.Now()
	if resp := other.Post(t, "settings.notifications.off", url.Values{"device": {id[1]}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("turning off: %s", resp.Status)
	}
	awaitLogins(t, a, 1)
	if waited := time.Since(start); waited > idle/4 {
		t.Fatalf("the kept login lasted %v after the device went off", waited)
	}
}
