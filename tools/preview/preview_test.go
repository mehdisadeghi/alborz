//go:build preview

package preview_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"golang.org/x/net/websocket"

	. "git.mehdix.org/alborz/internal/alborztest"
)

var (
	routes = flag.String("route", "mail", "comma-separated route names (mail, tasks, journal, contacts, calendar, day, settings, filters, collection) or paths")
	widths = flag.String("width", "1280,390", "comma-separated viewport widths; below 600 emulates a phone")
	height = flag.Int("height", 900, "viewport height")
	out    = flag.String("out", filepath.Join(os.TempDir(), "alborz-preview"), "directory for <route>-<width>.png")
	script = flag.String("js", "", "file of JavaScript evaluated before each shot; its result is printed")
	chrome = flag.String("chrome", "", "Chrome or chrome-headless-shell binary; default: found on PATH or in /Applications")
	port   = flag.Int("port", 9233, "Chrome's remote debugging port")
)

func TestPreview(t *testing.T) {
	// The domain's DAV server is another, so the rails show collections in both places.
	d := DAV(t, Calendar(DAVHome+"work/", "Work"), Book(DAVBooks+"team/", "Team"))
	a, c := SignedIn(t, User, WithDAV(d), WithSieve(Sieve(t)))
	calendar, tasks, journal, book := c.CreateCalendar(t, "Plans", ""), c.CreateCalendar(t, "Tasks", "tasks"), c.CreateCalendar(t, "Journal", "journal"), c.CreateBook(t, "People")
	// The event falls today, so the current month's pages always show it.
	today := time.Now().UTC()
	a.Put(t, calendar+"record.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:event\r\nDTSTAMP:20261027T080000Z\r\nDTSTART:"+today.Format("20060102")+"T120000Z\r\nDTEND:"+today.Format("20060102")+"T130000Z\r\nRRULE:FREQ=WEEKLY\r\nSUMMARY:Review a long product design proposal\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	for i, span := range []struct {
		name        string
		start, days int
	}{{"Factory visit", -2, 9}, {"Production trial", 0, 4}, {"Design workshop", 1, 2}, {"Supplier review", 1, 1}} {
		start := today.AddDate(0, 0, span.start)
		a.Put(t, calendar+fmt.Sprintf("span%d.ics", i), "text/calendar", fmt.Sprintf("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:span%d\r\nDTSTAMP:20261027T080000Z\r\nDTSTART;VALUE=DATE:%s\r\nDTEND;VALUE=DATE:%s\r\nSUMMARY:%s\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n", i, start.Format("20060102"), start.AddDate(0, 0, span.days).Format("20060102"), span.name))
	}
	for i := range 6 {
		a.Put(t, tasks+fmt.Sprintf("t%d.ics", i), "text/calendar", fmt.Sprintf("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VTODO\r\nUID:task%d\r\nDTSTAMP:20261027T080000Z\r\nCREATED:20261020T080000Z\r\nSUMMARY:Review a very long product design proposal with many words in its summary number %d\r\nDUE;VALUE=DATE:20261028\r\nEND:VTODO\r\nEND:VCALENDAR\r\n", i, i))
	}
	a.Put(t, tasks+"child.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VTODO\r\nUID:child\r\nDTSTAMP:20261027T080000Z\r\nDTSTART;VALUE=DATE:20261027\r\nRRULE:FREQ=WEEKLY\r\nSUMMARY:Child task\r\nRELATED-TO:task0\r\nEND:VTODO\r\nEND:VCALENDAR\r\n")
	a.Put(t, journal+"record.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VJOURNAL\r\nUID:journal\r\nDTSTAMP:20261027T080000Z\r\nDTSTART;VALUE=DATE:20261027\r\nSUMMARY:Review a long product design proposal\r\nDESCRIPTION:Some text\r\nEND:VJOURNAL\r\nEND:VCALENDAR\r\n")
	a.Put(t, book+"record.vcf", "text/vcard", "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:contact\r\nFN:Alex Product Design Example\r\nN:Example;Alex;;;\r\nEMAIL:alex@example.org\r\nTEL:+49 123456789\r\nEND:VCARD\r\n")
	// Sent to many, so the Sent list shows where its recipients are cut.
	a.IMAP.Append(t, "Sent", "From: <"+User+">\r\nTo: Alice Example <alice@example.org>, Bob Example <bob@example.org>, "+
		"Carol Example <carol@example.org>, \"Sadeghi, Mehdi\" <mehdi@example.org>, سارا احمدی <sara@example.org>\r\n"+
		"Subject: Plans for the week\r\nDate: Mon, 02 Nov 2026 09:00:00 +0000\r\nMessage-ID: <many@test>\r\nContent-Type: text/plain\r\n\r\nHello all.\r\n")
	day := today.AddDate(0, 0, 7).Format("20060102")
	invite := a.IMAP.Append(t, "INBOX", "From: Alice Example <alice@example.org>\r\nTo: <"+User+">\r\nSubject: Design review\r\n"+
		"Date: Mon, 02 Nov 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/calendar; charset=utf-8; method=REQUEST\r\n\r\n"+
		"BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nPRODID:-//test//EN\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:invite\r\nDTSTAMP:20261102T100000Z\r\n"+
		"DTSTART:"+day+"T140000Z\r\nDTEND:"+day+"T150000Z\r\nSUMMARY:Design review\r\nORGANIZER:mailto:alice@example.org\r\n"+
		"ATTENDEE;PARTSTAT=ACCEPTED:mailto:alice@example.org\r\nATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:"+User+"\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	draft := a.IMAP.Append(t, "Drafts", "From: <"+User+">\r\nTo: Bob Example <bob@example.org>\r\nSubject: Notes\r\n"+
		"Date: Mon, 02 Nov 2026 11:00:00 +0000\r\nMessage-ID: <draft@test>\r\nContent-Type: text/plain\r\n\r\nUnfinished.\r\n", imap.FlagDraft, imap.FlagSeen)
	for _, name := range []string{"main", "holiday"} {
		c.Post(t, "filters.list", url.Values{"name": {name}, "content": {"keep;\r\n"}}, url.Values{"account": {User}})
	}
	c.Post(t, "filters.filter.activate", url.Values{}, "main", url.Values{"account": {User}})
	if uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX")); len(uids) > 0 {
		c.Post(t, "mail.messages.flag", url.Values{"uids": {uids[0]}, "color": {"red"}, "next": {"/"}}, "INBOX")
	}
	c.SignIn(t, User2)
	named := map[string]string{
		"mail": a.Path("mail.mailbox", "INBOX"), "sent": a.Path("mail.mailbox", "Sent", url.Values{"account": {User}}), "tasks": a.Path("calendar.tasks"), "journal": a.Path("calendar.journal"),
		"contacts": a.Path("contacts.list"), "calendar": a.Path("calendar.month"), "day": a.Path("calendar.day", url.Values{"date": {today.Format(time.DateOnly)}}),
		"settings": a.Path("settings.profile"), "filters": a.Path("filters.list", url.Values{"account": {User}}),
		"invite": a.Path("mail.message", "INBOX", invite, url.Values{"account": {User}}), "draft": a.Path("mail.message", "Drafts", draft, url.Values{"account": {User}}),
		"reply": a.Path("mail.message.reply", "Drafts", draft, url.Values{"account": {User}}), "compose": a.Path("mail.compose"),
		"collection": a.Path("calendar.collection", d.At()+DAVHome+"work/", url.Values{"account": {User}}),
	}
	var js string
	if *script != "" {
		b, err := os.ReadFile(*script)
		if err != nil {
			t.Fatal(err)
		}
		js = string(b)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		t.Fatal(err)
	}
	b := startChrome(t)
	cookie := c.Cookie("alborz_session")
	for route := range strings.SplitSeq(*routes, ",") {
		path := route
		if p, ok := named[route]; ok {
			path = p
		}
		for w := range strings.SplitSeq(*widths, ",") {
			width, err := strconv.Atoi(w)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(*out, strings.Trim(strings.NewReplacer("/", "-", "?", "-", "=", "-", "&", "-").Replace(route), "-")+"-"+w+".png")
			b.shot(t, a.URL, path, cookie, width, js, file)
			t.Log(file)
		}
	}
}

type browser struct{ port int }

func startChrome(t *testing.T) browser {
	bin := *chrome
	if bin == "" {
		bin = findChrome(t)
	}
	cmd := exec.Command(bin, "--headless=new", "--remote-allow-origins=*", "--hide-scrollbars", "--remote-debugging-port="+strconv.Itoa(*port), "--user-data-dir="+t.TempDir(), "about:blank")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	b := browser{*port}
	for range 100 {
		if resp, err := http.Get(b.url("/json/version")); err == nil {
			resp.Body.Close()
			return b
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("chrome did not answer on port %d", *port)
	return b
}

func findChrome(t *testing.T) string {
	for _, name := range []string{"chrome-headless-shell", "google-chrome", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, p := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatal("no Chrome found; pass -chrome")
	return ""
}

func (b browser) url(path string) string { return "http://127.0.0.1:" + strconv.Itoa(b.port) + path }

// shot opens a tab, signs it in, loads path at width, runs js and saves a PNG.
func (b browser) shot(t *testing.T, base, path string, cookie *http.Cookie, width int, js, file string) {
	req, _ := http.NewRequest(http.MethodPut, b.url("/json/new?about:blank"), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tab struct{ ID, WebSocketDebuggerURL string }
	err = json.NewDecoder(resp.Body).Decode(&tab)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer http.Get(b.url("/json/close/" + tab.ID))
	ws, err := websocket.Dial(tab.WebSocketDebuggerURL, "", "http://127.0.0.1/")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	cdp := devtools{ws: ws}
	phone := width < 600
	cdp.call(t, "Network.setCookie", map[string]any{"name": cookie.Name, "value": cookie.Value, "url": base, "path": "/"})
	// A browser signs in through a page whose helpers.js writes its zone; signed in by cookie, the tab writes it here.
	zone, _ := cdp.eval(t, "Intl.DateTimeFormat().resolvedOptions().timeZone").(string)
	cdp.call(t, "Network.setCookie", map[string]any{"name": "alborz_tz", "value": zone, "url": base, "path": "/"})
	cdp.call(t, "Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": *height, "deviceScaleFactor": 1, "mobile": phone})
	cdp.call(t, "Emulation.setTouchEmulationEnabled", map[string]any{"enabled": phone, "maxTouchPoints": 1})
	cdp.call(t, "Page.navigate", map[string]any{"url": base + path})
	for range 80 {
		if ready, _ := cdp.eval(t, `document.readyState === "complete" && !!document.querySelector("main")`).(bool); ready {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond)
	if js != "" {
		v, _ := json.MarshalIndent(cdp.eval(t, js), "", " ")
		t.Logf("%s %d: %s", path, width, v)
	}
	var png struct{ Data string }
	cdp.result(t, "Page.captureScreenshot", map[string]any{"format": "png"}, &png)
	data, err := base64.StdEncoding.DecodeString(png.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// devtools speaks the Chrome DevTools Protocol over one tab's socket.
type devtools struct {
	ws *websocket.Conn
	id int
}

func (d *devtools) call(t *testing.T, method string, params any) { d.result(t, method, params, nil) }

func (d *devtools) result(t *testing.T, method string, params, into any) {
	d.id++
	if err := websocket.JSON.Send(d.ws, map[string]any{"id": d.id, "method": method, "params": params}); err != nil {
		t.Fatal(err)
	}
	for {
		var m struct {
			ID     int
			Result json.RawMessage
			Error  *struct{ Message string }
		}
		if err := websocket.JSON.Receive(d.ws, &m); err != nil {
			t.Fatal(err)
		}
		if m.ID != d.id {
			continue
		}
		if m.Error != nil {
			t.Fatal(errors.New(method + ": " + m.Error.Message))
		}
		if into != nil {
			if err := json.Unmarshal(m.Result, into); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
}

func (d *devtools) eval(t *testing.T, js string) any {
	var r struct {
		Result           struct{ Value any }
		ExceptionDetails any
	}
	d.result(t, "Runtime.evaluate", map[string]any{"expression": js, "returnByValue": true, "awaitPromise": true}, &r)
	if r.ExceptionDetails != nil {
		t.Fatalf("js: %v", r.ExceptionDetails)
	}
	return r.Result.Value
}
