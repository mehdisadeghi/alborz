package web

import (
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

type loginHeld struct {
	imapserver.SessionIMAP4rev2
	hold func(string)
}

func (s loginHeld) Login(user, password string) error {
	if s.hold != nil {
		s.hold(user)
	}
	return s.SessionIMAP4rev2.Login(user, password)
}

func rememberedServer(t *testing.T, hold func(string)) *Server {
	t.Helper()
	mem := imapmemserver.New()
	for _, user := range []string{"one@test.localdomain", "two@test.localdomain"} {
		mem.AddUser(imapmemserver.NewUser(user, "password"))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	imap := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return loginHeld{mem.NewSession().(imapserver.SessionIMAP4rev2), hold}, nil, nil
		},
		Caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}}, InsecureAuth: true,
	})
	go imap.Serve(listener)
	t.Cleanup(func() { imap.Close() })
	core, err := alborz.New(log.New(io.Discard, "", 0), alborz.Options{
		LoginKey: "YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=", DataDir: t.TempDir(),
		Domains: map[string]alborz.DomainConfig{"test.localdomain": {"imap": "imap+insecure://" + listener.Addr().String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	return &Server{Server: core, Sessions: newSessions(core.SessionRecords, core.Pushes)}
}

func rememberedContext(t *testing.T, server *Server, accounts ...string) *Context {
	t.Helper()
	s, secret := newSession()
	server.Sessions.put(s)
	ctx := &Context{Server: server, session: s, sessionSecret: secret,
		request: httptest.NewRequest(http.MethodGet, "/private", nil), response: &Response{ResponseWriter: httptest.NewRecorder()}}
	for _, account := range accounts {
		if err := ctx.SetLoginToken(account, "password"); err != nil {
			t.Fatal(err)
		}
	}
	return ctx
}

type failedSessionSave struct {
	alborz.SessionRecords
	err error
}

func (s failedSessionSave) Save(*alborz.SessionRecord) error { return s.err }

func TestPasswordChangeSignsOutOtherBrowsersWhenSavingFails(t *testing.T) {
	server := rememberedServer(t, nil)
	ctx := rememberedContext(t, server, "one@test.localdomain")
	other := rememberedContext(t, server, "two@test.localdomain")
	oldLogin, err := server.SignIn("one@test.localdomain", "password", "")
	if err != nil {
		t.Fatal(err)
	}
	other.session.add(oldLogin)
	failed := errors.New("session store full")
	server.Sessions.records = failedSessionSave{server.Sessions.records, failed}
	if err := ctx.SignOutOthers("one@test.localdomain"); !errors.Is(err, failed) {
		t.Fatalf("signing the other browsers out: %v", err)
	}
	if oldLogin.Alive() || server.Sessions.Holds(other.session.ID, "one@test.localdomain") {
		t.Fatal("failed persistence left another browser signed in after password change")
	}
}

func TestBrowserLogoutPreservesAnotherUnrememberedAccount(t *testing.T) {
	server := rememberedServer(t, nil)
	ctx := rememberedContext(t, server, "one@test.localdomain")
	for _, account := range []string{"one@test.localdomain", "two@test.localdomain"} {
		login, err := server.SignIn(account, "password", "")
		if err != nil {
			t.Fatal(err)
		}
		ctx.session.add(login)
	}
	id, secret := ctx.session.ID, ctx.sessionSecret
	remaining := ctx.LogoutAccount("one@test.localdomain")
	if remaining == nil || remaining.Username() != "two@test.localdomain" || !server.Sessions.Holds(id, "two@test.localdomain") {
		t.Fatal("logging out the remembered account also discarded the unremembered account")
	}
	if _, ok := server.SessionRecords.Load(id); ok {
		t.Fatal("logged-out credentials remained persisted")
	}
	if server.Sessions.lookup(id, secret) == nil {
		t.Fatal("the remaining account cannot be reached on the next request")
	}
	if ctx.LogoutAccount("two@test.localdomain") != nil {
		t.Fatal("last logout retained an account")
	}
	if server.Sessions.lookup(id, secret) != nil {
		t.Fatal("last logout did not end the browser session")
	}
}

func TestConcurrentRestoreCannotReAddARevokedAccount(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var hold atomic.Bool
	server := rememberedServer(t, func(user string) {
		if user == "one@test.localdomain" && hold.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
	})
	ctx := rememberedContext(t, server, "one@test.localdomain", "two@test.localdomain")
	hold.Store(true)
	done := make(chan bool, 1)
	go func() { done <- ctx.RestoreRememberedAccounts() }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("restore never reached upstream authentication")
	}
	if err := server.Sessions.SignOut(ctx.session.ID, "one@test.localdomain"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case restored := <-done:
		if !restored {
			t.Fatal("unrevoked account did not restore")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not finish")
	}
	if server.Sessions.Holds(ctx.session.ID, "one@test.localdomain") {
		t.Fatal("in-flight restoration re-added the revoked account")
	}
	if !server.Sessions.Holds(ctx.session.ID, "two@test.localdomain") {
		t.Fatal("revocation removed the other account")
	}
	for _, login := range server.Logins() {
		if login.Username() == "one@test.localdomain" && login.Alive() {
			t.Fatal("discarded restoration left an upstream login alive")
		}
	}
}

func TestRemovingALiveAccountRevokesItsPendingRestoration(t *testing.T) {
	server := rememberedServer(t, nil)
	ctx := rememberedContext(t, server, "one@test.localdomain", "two@test.localdomain")
	login, err := server.SignIn("one@test.localdomain", "password", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx.session.add(login)
	sealed := ctx.session.remember["one@test.localdomain"]
	pending, err := server.SignIn("one@test.localdomain", "password", "")
	if err != nil {
		t.Fatal(err)
	}
	defer pending.Close()
	removed := ctx.session.remove("one@test.localdomain")
	if removed == nil {
		t.Fatal("live login was not removed")
	}
	defer removed.Close()
	// A restore may finish before the remover has closed its upstream connection or saved the record.
	if ctx.session.restored(pending, "one@test.localdomain", sealed) {
		t.Fatal("removing the live login left a credential that could restore it")
	}
}

// A session an older build kept remembers the address as typed; restoring it signs the account in (ADR 108).
func TestRestoringAnAddressAsTypedSignsItsAccountIn(t *testing.T) {
	const typed, account = "One@Test.localdomain", "one@test.localdomain"
	server := rememberedServer(t, nil)
	ctx := rememberedContext(t, server, typed)
	if !ctx.RestoreRememberedAccounts() || ctx.Login.Username() != account {
		t.Fatalf("restoring %s did not sign %s in", typed, account)
	}
	if !server.Sessions.Holds(ctx.session.ID, account) || len(ctx.session.remember[account]) == 0 || ctx.session.remember[typed] != nil {
		t.Fatalf("the session remembers %v, want %s alone", ctx.session.remember, account)
	}
}

func TestConcurrentRestorationSurvivesRenewalAndAnotherRestore(t *testing.T) {
	for _, action := range []string{"renew", "restore"} {
		t.Run(action, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			var hold atomic.Bool
			server := rememberedServer(t, func(user string) {
				if user == "one@test.localdomain" && hold.CompareAndSwap(true, false) {
					close(entered)
					<-release
				}
			})
			ctx := rememberedContext(t, server, "one@test.localdomain", "two@test.localdomain")
			other := &Context{Server: server, session: ctx.session, sessionSecret: ctx.sessionSecret,
				request: httptest.NewRequest(http.MethodGet, "/private", nil), response: &Response{ResponseWriter: httptest.NewRecorder()}}
			hold.Store(true)
			done := make(chan bool, 1)
			go func() { done <- ctx.RestoreRememberedAccounts() }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("restore never reached authentication")
			}
			if action == "renew" {
				if err := other.renewRemembered(time.Now()); err != nil {
					t.Fatal(err)
				}
			} else if !other.RestoreRememberedAccounts() {
				t.Fatal("second restore did not succeed")
			}
			close(release)
			select {
			case restored := <-done:
				if !restored {
					t.Fatal("concurrent work discarded valid restored accounts")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("restore did not finish")
			}
			for _, account := range []string{"one@test.localdomain", "two@test.localdomain"} {
				if !server.Sessions.Holds(ctx.session.ID, account) {
					t.Fatalf("lost %s", account)
				}
			}
			alive := 0
			for _, login := range server.Logins() {
				if login.Alive() {
					alive++
				}
			}
			if alive != 2 {
				t.Fatalf("%d upstream logins still alive, want one per account", alive)
			}
		})
	}
}
