package main

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/fernet/fernet-go"
)

const (
	testUser = "a@test.local"
	testPass = "x"
)

var testKey = fernet.MustDecodeKeys("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")[0]

func startIMAP(t *testing.T) string {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(testUser, testPass)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
	})
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// serve mounts the routes as run does over the data in dir; stop closes the server so another can open dir.
func serve(t *testing.T, dir, imapAddr string) (s *web.Server, base string, stop func()) {
	t.Helper()
	s, err := web.New(log.New(io.Discard, "", 0), &alborz.Options{
		Upstreams: []string{"test.local=imap+insecure://" + imapAddr},
		LoginKey:  testKey,
		DataDir:   dir,
	}, mail.TemplateFuncs)
	if err != nil {
		t.Fatal(err)
	}
	if err := mount(s); err != nil {
		t.Fatal(err)
	}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	stop = sync.OnceFunc(func() {
		ts.Close()
		s.Close()
	})
	t.Cleanup(stop)
	return s, ts.URL, stop
}

// client shows each answer as it is, a redirect too.
var client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

// lockOpen are the routes a locked session reaches: what a browser fetches before signing in, signing
// in, the way back in, and DAV, which asks for a password on every request.
var lockOpen = map[string]bool{
	"core.manifest": true, "core.worker": true, "core.asset": true,
	"auth.login": true, "auth.unlock": true, "auth.unlock.begin": true,
	"auth.unlock.finish": true, "auth.unlock.leave": true,
	"dav.caldav": true, "dav.carddav": true, "dav.published": true, "dav.collections": true,
}

var wildcard = regexp.MustCompile(`\{[^}]*\}`)

// Every route but the few open ones answers a locked session with the lock, so a new route is locked
// unless it is named open here.
func TestALockedSessionReachesOnlyTheWayBackIn(t *testing.T) {
	imapAddr, dir := startIMAP(t), t.TempDir()

	s, base, stop := serve(t, dir, imapAddr)
	resp, err := client.PostForm(base+"/login",
		url.Values{"username": {testUser}, "password": {testPass}, "remember-me": {"on"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "alborz_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("signing in set no session cookie (status %d)", resp.StatusCode)
	}
	id, _, _ := strings.Cut(session.Value, ".")
	rec, ok := s.SessionRecords.Load(id)
	if !ok {
		t.Fatal("signing in with remember-me kept no session")
	}
	rec.Lock.Passkeys = []alborz.Passkey{{Name: "key"}}
	stop()

	// A session with a passkey comes back from a restart locked. Each probe starts from it again,
	// since leaving ends it.
	s, base, _ = serve(t, dir, imapAddr)
	routes := s.Routes()
	if len(routes) < 100 {
		t.Fatalf("only %d routes mounted; the check would pass on too few", len(routes))
	}
	named := map[string]bool{}
	for _, r := range routes {
		named[r.Name] = true
		path := wildcard.ReplaceAllString(strings.ReplaceAll(r.Pattern, "{$}", ""), "x")
		for _, method := range methods(r.Method) {
			if err := s.SessionRecords.Save(rec); err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequest(method, base+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.AddCookie(session)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			to, _ := resp.Location()
			locked := resp.StatusCode == http.StatusSeeOther && to != nil && to.Path == "/unlock"
			if locked == lockOpen[r.Name] {
				t.Errorf("%s %s (%q): %d to %v, locked=%v", method, path, r.Name, resp.StatusCode, to, locked)
			}
		}
	}
	for name := range lockOpen {
		if !named[name] {
			t.Errorf("no route named %q", name)
		}
	}
}

func methods(m string) []string {
	if m == "" {
		return []string{http.MethodGet, http.MethodPost}
	}
	return []string{m}
}
