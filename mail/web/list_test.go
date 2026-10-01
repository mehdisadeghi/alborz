package web_test

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/emersion/go-imap/v2"
)

// The login must still work after answering the bad query.
func TestUnknownSearchKeySurvives(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	if body := c.Page(t, "mail.mailbox", "INBOX", url.Values{"query": {"foo:bar"}}); body == "" {
		t.Fatal("the unrecognised key produced no page")
	}
	if body := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(body, "message-row") {
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
		if !strings.Contains(c.Follow(t, next), "message-flag-form") {
			t.Fatal("the message page did not render after the flag change")
		}
	}
	if body := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(body, "message-row") {
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
	fragment, _ := read(path, "message-navigation")
	if !strings.Contains(fragment, `id="message-navigation"`) || strings.Contains(fragment, "<!DOCTYPE") {
		t.Fatal("navigation endpoint did not return a fragment")
	}
	a.IMAP.Close()
	c.HTTP.Timeout = time.Second
	body, timing := read(path, "body")
	if !strings.Contains(body, "message-flag-form") || !strings.Contains(timing, "body_hit") || strings.Contains(timing, "imap_work") {
		t.Fatalf("cached message used upstream: %s", timing)
	}
	body, _ = read(a.Path("mail.sidebar", url.Values{"path": {a.Path("mail.message", "INBOX", uids[0])}}), "aside")
	if !strings.Contains(body, "data-mail-sidebar") || strings.Contains(body, "message-flag-form") {
		t.Fatal("sidebar response rendered the message")
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
	for _, cls := range strings.Fields(page[start : start+end]) {
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
		"Mon, 1 Jan 4000 10:00:00 +0000": "January 1, 4000",
		"Fri, 1 Jan 0560 10:00:00 +0000": "January 1, 560",
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

// Both offers are followed as the browser would, relative to the page that drew them.
func TestFolderSearchOffersEveryFolder(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	offer := func(page, from, label string) string {
		t.Helper()
		m := regexp.MustCompile(`<a class="filter-offer" href="([^"]*)">` + label + `</a>`).FindStringSubmatch(page)
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
	everywhere := offer(c.Follow(t, inbox), inbox, "Search all folders")
	results := c.Follow(t, everywhere)
	if got := folders(results); !slices.Equal(got, want) {
		t.Fatalf("all folders found eve in %v, want %v", got, want)
	}
	whole := c.Follow(t, offer(results, everywhere, "Search the whole message too"))
	if got := folders(whole); !slices.Equal(got, want) {
		t.Errorf("the whole message found eve in %v, want %v", got, want)
	}
	if strings.Contains(whole, `class="filter-offer"`) {
		t.Error("the whole-message search still offers the whole message")
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

func TestDeleteNoticeCountsTheRest(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) < 3 {
		t.Fatalf("need three seeded messages, found %d", len(uids))
	}
	c.Post(t, "mail.messages.delete", url.Values{"uids": uids[:2]}, "INBOX", url.Values{"ipp": {"2"}})
	body := c.Page(t, "mail.mailbox", "INBOX")
	action, fields := NoticeAction(body)
	if fields != nil || action != a.Path("mail.mailbox.empty", "INBOX") {
		t.Fatalf("a full page's notice offers %q %v", action, fields)
	}
	rest := fmt.Sprintf("%d", len(uids)-2)
	if !strings.Contains(body, "Delete all "+rest+" in this folder") {
		t.Errorf("the notice does not count the rest: %s", NoticeBar(body))
	}
	if page := c.Follow(t, action); !strings.Contains(page, "It holds "+rest+" messages") {
		t.Errorf("the empty page does not say what it holds: %s", AlertText(page))
	}
	c.Post(t, "mail.messages.delete", url.Values{"uids": uids[2:3]}, "INBOX", url.Values{"ipp": {"2"}})
	if action, _ := NoticeAction(c.Page(t, "mail.mailbox", "INBOX")); action != "" {
		t.Errorf("a partial page's notice offers %q", action)
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
		if want := fmt.Sprintf("3–4 of %d", len(all)); !strings.Contains(body, want) {
			t.Errorf("%s: the second page does not say %q", route, want)
		}
		if got := PagerSteps(t, body); !slices.Equal(got, []string{"", "2"}) {
			t.Errorf("%s: the second page steps to pages %q, want the first and the third", route, got)
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
	subject := regexp.MustCompile(`class="message-list-subject[^"]*" role="cell">\s*<a [^>]*href="([^"]+)"`)
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
		links := subject.FindAllStringSubmatch(c.Page(t, "mail.mailbox", "INBOX", at.list), -1)
		if len(links) <= at.row {
			t.Fatalf("%v lists %d rows", at.list, len(links))
		}
		msg := c.Follow(t, html.UnescapeString(links[at.row][1]))
		if prev, next := step(msg, "k"), step(msg, "j"); prev != all[at.pos-1] || next != all[at.pos+1] {
			t.Errorf("%v: row %d steps to %q and %q, want %q and %q", at.list, at.row, prev, next, all[at.pos-1], all[at.pos+1])
		}
		if !strings.Contains(msg, fmt.Sprintf(">%d of 6<", at.pos+1)) {
			t.Errorf("%v: row %d does not say it is %d of 6", at.list, at.row, at.pos+1)
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
	for _, list := range [][]any{{"mail.mailbox", "INBOX"}, {"mail.search", url.Values{"query": {"example"}}}} {
		route := list[0].(string)
		// The loss is noticed a moment after Close returns; until then the cache answers.
		var body string
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			c.Post(t, "mail.mailbox.refresh", nil, "INBOX")
			body = c.Page(t, route, list[1:]...)
			if strings.Contains(body, "did not answer") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: nothing says a server did not answer: %s", route, NoticeBar(body))
			}
		}
		if !strings.Contains(NoticeBar(body), Elsewhere) {
			t.Errorf("%s: the notice does not name the account: %s", route, NoticeBar(body))
		}
		if rows := rowAccounts(body); rows[User] == 0 || rows[Elsewhere] != 0 {
			t.Errorf("%s: rows by account are %v", route, rows)
		}
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
		for _, m := range regexp.MustCompile(`class="message-list-subject[^"]*" role="cell">\s*<a [^>]*href="([^"]+)"`).FindAllStringSubmatch(body, -1) {
			links = append(links, m[1])
		}
	}
	if len(order) != 5 || len(links) != 5 || slices.IsSortedFunc(order, func(a, b string) int { return strings.Compare(b, a) }) {
		t.Fatalf("three pages of one conversation each list %v, want the five seeded messages out of the folder's order", order)
	}
	msg := c.Follow(t, html.UnescapeString(links[2]))
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
	link := regexp.MustCompile(`class="message-list-subject[^"]*" role="cell">\s*<a [^>]*href="([^"]+)"`).FindStringSubmatch(page)
	if link == nil {
		t.Fatal("the third page has no row to open")
	}
	msg := c.Follow(t, html.UnescapeString(link[1]))
	if prev, next := neighbourUID(t, msg, "k"), neighbourUID(t, msg, "j"); prev != order[1] || next != order[3] {
		t.Errorf("the third message steps to %q and %q, want %q and %q", prev, next, order[1], order[3])
	}
	if !strings.Contains(msg, ">3 of 5<") {
		t.Error("the third message does not say it is 3 of 5")
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
	if bar := NoticeBar(page); !strings.Contains(bar, "The server refused one.") {
		t.Errorf("the notice does not count the refused message:\n%s", bar)
	}
}
