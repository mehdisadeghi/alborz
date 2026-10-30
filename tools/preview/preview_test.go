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

	"golang.org/x/net/websocket"

	. "git.mehdix.org/alborz/internal/alborztest"
)

var (
	routes = flag.String("route", "mail", "comma-separated route names (mail, tasks, journal, contacts, calendar, day, settings, filters) or paths")
	widths = flag.String("width", "1280,390", "comma-separated viewport widths; below 600 emulates a phone")
	height = flag.Int("height", 900, "viewport height")
	out    = flag.String("out", filepath.Join(os.TempDir(), "alborz-preview"), "directory for <route>-<width>.png")
	script = flag.String("js", "", "file of JavaScript evaluated before each shot; its result is printed")
	chrome = flag.String("chrome", "", "Chrome or chrome-headless-shell binary; default: found on PATH or in /Applications")
	port   = flag.Int("port", 9233, "Chrome's remote debugging port")
)

func TestPreview(t *testing.T) {
	a, c := SignedIn(t, User, WithDAVHere(), WithSieve(Sieve(t)))
	calendar, tasks, journal, book := c.CreateCalendar(t, "Plans", ""), c.CreateCalendar(t, "Tasks", "tasks"), c.CreateCalendar(t, "Journal", "journal"), c.CreateBook(t, "People")
	a.Put(t, calendar+"record.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:event\r\nDTSTAMP:20261027T080000Z\r\nDTSTART:20261027T100000Z\r\nDTEND:20261027T110000Z\r\nSUMMARY:Review a long product design proposal\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	for i := range 6 {
		a.Put(t, tasks+fmt.Sprintf("t%d.ics", i), "text/calendar", fmt.Sprintf("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VTODO\r\nUID:task%d\r\nDTSTAMP:20261027T080000Z\r\nCREATED:20261020T080000Z\r\nSUMMARY:Review a very long product design proposal with many words in its summary number %d\r\nDUE;VALUE=DATE:20261028\r\nEND:VTODO\r\nEND:VCALENDAR\r\n", i, i))
	}
	a.Put(t, tasks+"child.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VTODO\r\nUID:child\r\nDTSTAMP:20261027T080000Z\r\nSUMMARY:Child task\r\nRELATED-TO:task0\r\nEND:VTODO\r\nEND:VCALENDAR\r\n")
	a.Put(t, journal+"record.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VJOURNAL\r\nUID:journal\r\nDTSTAMP:20261027T080000Z\r\nDTSTART;VALUE=DATE:20261027\r\nSUMMARY:Review a long product design proposal\r\nDESCRIPTION:Some text\r\nEND:VJOURNAL\r\nEND:VCALENDAR\r\n")
	a.Put(t, book+"record.vcf", "text/vcard", "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:contact\r\nFN:Alex Product Design Example\r\nN:Example;Alex;;;\r\nEMAIL:alex@example.org\r\nTEL:+49 123456789\r\nEND:VCARD\r\n")
	if uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX")); len(uids) > 0 {
		c.Post(t, "mail.messages.flag", url.Values{"uids": {uids[0]}, "color": {"red"}, "next": {"/"}}, "INBOX")
	}
	c.SignIn(t, User2)
	named := map[string]string{
		"mail": a.Path("mail.mailbox", "INBOX"), "tasks": a.Path("calendar.tasks"), "journal": a.Path("calendar.journal"),
		"contacts": a.Path("contacts.list"), "calendar": a.Path("calendar.month"), "day": a.Path("calendar.day", url.Values{"date": {"2026-10-27"}}),
		"settings": a.Path("settings.profile"), "filters": a.Path("filters.list"),
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
