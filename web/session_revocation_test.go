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
	for _, user := range []string{"one@test.local", "two@test.local"} {
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
		Upstreams: []string{"test.local=imap+insecure://" + listener.Addr().String()},
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

func TestPasswordChangeRevokesOtherSessionsWhenRememberingFails(t *testing.T) {
	server := rememberedServer(t, nil)
	ctx := rememberedContext(t, server, "one@test.local")
	other := rememberedContext(t, server)
	oldLogin, err := server.SignIn("one@test.local", "password", "")
	if err != nil {
		t.Fatal(err)
	}
	other.session.add(oldLogin)
	newLogin, err := server.SignIn("one@test.local", "password", "")
	if err != nil {
		t.Fatal(err)
	}
	failed := errors.New("session store full")
	server.Sessions.records = failedSessionSave{server.Sessions.records, failed}
	if err := ctx.PasswordChanged(newLogin, "new password"); !errors.Is(err, failed) {
		t.Fatalf("password change error: %v", err)
	}
	if oldLogin.Alive() || server.Sessions.Holds(other.session.ID, "one@test.local") {
		t.Fatal("failed persistence left another browser signed in after password change")
	}
}

func TestBrowserLogoutPreservesAnotherUnrememberedAccount(t *testing.T) {
	server := rememberedServer(t, nil)
	ctx := rememberedContext(t, server, "one@test.local")
	for _, account := range []string{"one@test.local", "two@test.local"} {
		login, err := server.SignIn(account, "password", "")
		if err != nil {
			t.Fatal(err)
		}
		ctx.session.add(login)
	}
	id, secret := ctx.session.ID, ctx.sessionSecret
	remaining := ctx.LogoutAccount("one@test.local")
	if remaining == nil || remaining.Username() != "two@test.local" || !server.Sessions.Holds(id, "two@test.local") {
		t.Fatal("logging out the remembered account also discarded the unremembered account")
	}
	if _, ok := server.SessionRecords.Load(id); ok {
		t.Fatal("logged-out credentials remained persisted")
	}
	if server.Sessions.lookup(id, secret) == nil {
		t.Fatal("the remaining account cannot be reached on the next request")
	}
	if ctx.LogoutAccount("two@test.local") != nil {
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
		if user == "one@test.local" && hold.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
	})
	ctx := rememberedContext(t, server, "one@test.local", "two@test.local")
	hold.Store(true)
	done := make(chan bool, 1)
	go func() { done <- ctx.RestoreRememberedAccounts() }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("restore never reached upstream authentication")
	}
	if err := server.Sessions.SignOut(ctx.session.ID, "one@test.local"); err != nil {
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
	if server.Sessions.Holds(ctx.session.ID, "one@test.local") {
		t.Fatal("in-flight restoration re-added the revoked account")
	}
	if !server.Sessions.Holds(ctx.session.ID, "two@test.local") {
		t.Fatal("revocation removed the other account")
	}
	for _, login := range server.Logins() {
		if login.Username() == "one@test.local" && login.Alive() {
			t.Fatal("discarded restoration left an upstream login alive")
		}
	}
}

func TestRemovingALiveAccountRevokesItsPendingRestoration(t *testing.T) {
	server := rememberedServer(t, nil)
	ctx := rememberedContext(t, server, "one@test.local", "two@test.local")
	login, err := server.SignIn("one@test.local", "password", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx.session.add(login)
	sealed := ctx.session.remember["one@test.local"]
	pending, err := server.SignIn("one@test.local", "password", "")
	if err != nil {
		t.Fatal(err)
	}
	defer pending.Close()
	removed := ctx.session.remove("one@test.local")
	if removed == nil {
		t.Fatal("live login was not removed")
	}
	defer removed.Close()
	// A restore may finish before the remover has closed its upstream connection or saved the record.
	if ctx.session.restored(pending, sealed) {
		t.Fatal("removing the live login left a credential that could restore it")
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
				if user == "one@test.local" && hold.CompareAndSwap(true, false) {
					close(entered)
					<-release
				}
			})
			ctx := rememberedContext(t, server, "one@test.local", "two@test.local")
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
			for _, account := range []string{"one@test.local", "two@test.local"} {
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
