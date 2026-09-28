package alborz

import (
	"errors"
	"fmt"
	"html/template"
	"maps"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Account describes one signed-in account for the rail.
type Account struct {
	Username string
}

// accountLogins is the session's bag: every account this browser has
// signed into. An account whose login has ended is dropped and named
// for the page, rather than left in the list as if it were still here.
func (ctx *Context) accountLogins() []*Login {
	if ctx.accountsLoaded {
		return ctx.accounts
	}
	s := ctx.lookupSession()
	if s == nil {
		return nil
	}
	logins, lost := s.Accounts()
	ctx.lostAccounts = append(ctx.lostAccounts, lost...)
	ctx.accounts = logins
	ctx.accountsLoaded = true
	return logins
}

// byName puts accounts in one order wherever they are listed, the
// rail and every picker alike. Sign-in order is a history, not an
// order: an account signed in again moved to the end.
func byName(logins []*Login) {
	slices.SortStableFunc(logins, func(a, b *Login) int {
		return strings.Compare(strings.ToLower(a.username), strings.ToLower(b.username))
	})
}

// Accounts lists the signed-in accounts in rail order.
func (ctx *Context) Accounts() []Account {
	logins := ctx.accountLogins()
	accounts := make([]Account, len(logins))
	for i, s := range logins {
		accounts[i] = Account{Username: s.username}
	}
	return accounts
}

// Logins lists the live logins of every signed-in account.
func (ctx *Context) Logins() []*Login {
	return ctx.accountLogins()
}

// AddAccount puts the account in this browser's bag and makes it the
// request's own. A login for the same address is replaced.
func (ctx *Context) AddAccount(s *Login) {
	session := ctx.Session()
	first := !slices.ContainsFunc(ctx.accountLogins(), func(l *Login) bool { return l.username != s.username })
	session.Add(s)
	ctx.accountsLoaded = false
	ctx.Login = s
	ctx.DefaultLogin = s
	ctx.joinProfile(s, first)
	for _, ready := range ctx.Server.OnAccountReady {
		ready(ctx, s)
	}
}

// URLAccount returns the account selected by the request's account
// parameter, "" when none.
func (ctx *Context) URLAccount() string {
	return ctx.urlAccount
}

// readable undoes the escapes a query value does not need: RFC 3986
// allows ":", "@", "/", "?" and "," in a query, and addresses, paths
// and alborz's own URLs are most of what its links carry there.
var readable = strings.NewReplacer("%40", "@", "%2F", "/", "%3A", ":", "%3F", "?", "%2C", ",")

// QueryValue writes a value into a URL query the way a reader would
// type it, escaping only what would end or split it.
func QueryValue(v string) string {
	return readable.Replace(url.QueryEscape(v))
}

// Query encodes a whole query, keys in order, each value as
// QueryValue writes it.
func Query(q url.Values) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(q)) {
		for _, v := range q[k] {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(QueryValue(k) + "=" + QueryValue(v))
		}
	}
	return b.String()
}

// AccountPath appends the request's account parameter to path, so a flow
// started under one account redirects back into it.
func (ctx *Context) AccountPath(path string) string {
	if ctx.urlAccount == "" {
		return path
	}
	// The path may already carry a query - a redirect to a search, say -
	// and a second "?" makes the account part of the last value.
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "account=" + QueryValue(ctx.urlAccount)
}

// NextOr returns the local page a form asked to return to, or fallback
// when the request names none, names another host, or names something
// that is not an absolute path. A setting written from a page belongs
// to that page: the form carries where it was submitted from, and the
// handler comes back to it instead of a fixed landing page.
func (ctx *Context) NextOr(fallback string) string {
	if next := localPath(ctx.FormValue("next")); next != "" {
		return next
	}
	return fallback
}

// From is the list an object's page was opened from, when its row said
// so; empty otherwise. The object's own account is in the URL too, so
// the list's scope - merged or one account - cannot be read back from
// it, and the page returns here instead.
func (ctx *Context) From() string {
	return localPath(ctx.QueryParam("from"))
}

// localPath is raw when it is a path on this site, empty otherwise.
func localPath(raw string) string {
	if raw == "" {
		return ""
	}
	// A backslash is read as a slash by browsers, so "/\\host" would
	// leave the site the way "//host" does.
	u, err := url.Parse(raw)
	if err != nil || u.Host != "" || !strings.HasPrefix(u.Path, "/") ||
		strings.ContainsRune(raw, '\\') {
		return ""
	}
	return u.String()
}

// LoginFor returns the listed account's live login, nil when the
// account is not signed in.
func (ctx *Context) LoginFor(username string) *Login {
	for _, s := range ctx.accountLogins() {
		if s.username == username {
			return s
		}
	}
	return nil
}

// Logout closes the request's login, removes it from the account
// list, and returns the first remaining account's login. A nil return
// means no account is left and the cookies were cleared.
func (ctx *Context) Logout() *Login {
	return ctx.LogoutAccount(ctx.Login.username)
}

// LogoutAccount closes exactly the named account and takes it out of
// the bag. A nil return means the bag is empty and nobody is signed in.
func (ctx *Context) LogoutAccount(username string) *Login {
	s := ctx.lookupSession()
	if s == nil {
		return nil
	}
	target := s.Remove(username)
	if target == nil {
		return ctx.DefaultLogin
	}
	target.Close()
	ctx.accountsLoaded = false
	ctx.forgetLoginToken(target.username)
	remaining, _ := s.Accounts()
	if len(remaining) == 0 {
		ctx.Login = nil
		ctx.DefaultLogin = nil
		return nil
	}
	ctx.DefaultLogin = remaining[0]
	ctx.Login = ctx.DefaultLogin
	return ctx.DefaultLogin
}

// SetLoginToken keeps the account's password with this session, sealed
// under the browser's own secret, so a login that ends or a restart
// costs a reconnection rather than a login. What the browser holds is
// an id and a key, never a password.
func (ctx *Context) SetLoginToken(username, password string) {
	s := ctx.Session()
	sealed, err := ctx.seal(password)
	if err != nil {
		ctx.Logger().Printf("failed to keep %q signed in: %v", username, err)
		return
	}
	s.Remember(username, sealed)
	if err := ctx.Server.Sessions.Save(s); err != nil {
		ctx.Logger().Printf("failed to write the session: %v", err)
	}
}

// forgetLoginToken drops the account's password, so signing out is
// signing out.
func (ctx *Context) forgetLoginToken(username string) {
	s := ctx.lookupSession()
	if s == nil {
		return
	}
	s.forget(username)
	var err error
	if s.remembered() {
		err = ctx.Server.Sessions.Save(s)
	} else {
		err = ctx.Server.Sessions.Delete(s.ID)
	}
	if err != nil {
		ctx.Logger().Printf("failed to write the session: %v", err)
	}
}

const (
	// loginRetryAfter is how long a failed automatic sign-in is left
	// alone. Without it every request retries every remembered account,
	// so one unreachable server turns a browser reload - or a page's own
	// assets - into a burst of logins, which is what a provider counting
	// them refuses the account for.
	loginRetryAfter = 2 * time.Minute
)

// signInTries refusals within signInWindow pause the sign-in form for
// one reader: fail2ban's defaults (maxretry, findtime), so a mail server
// that trusts alborz to name the reader is asked no more than its own
// lockout would allow.
const (
	signInTries  = 5
	signInWindow = 10 * time.Minute
)

// PausedError is a sign-in not asked of the mail server: the reader has
// had signInTries refusals, and may try again at Until.
type PausedError struct{ Until time.Time }

func (e PausedError) Error() string {
	return fmt.Sprintf("too many refused passwords; paused until %v", e.Until.Format(time.RFC3339))
}

// SignIn opens a login for username as the mail server says, for a
// reader at from. Readers are counted by their address, or behind a
// proxy not named as trusted, where every reader has the proxy's, by
// address and account.
func (s *Server) SignIn(username, password, from string) (*Login, error) {
	var login *Login
	err := s.signInRefused.attempt(readerKey(from, username), func() (err error) {
		login, err = s.Logins.Put(username, password, from)
		return err
	})
	return login, err
}

// readerKey is whom a refused password is counted against.
func readerKey(from, username string) string {
	if requestAddress(from) == "" {
		return from + "\x00" + strings.ToLower(username)
	}
	return from
}

// SignInRefusal says why a sign-in failed, in the reader's language,
// and with what status a form is answered. An empty text is alborz
// itself failing, which is the caller's to raise as an error.
func (ctx *Context) SignInRefusal(err error) (string, int) {
	var paused PausedError
	if errors.As(err, &paused) {
		minutes := int(math.Ceil(time.Until(paused.Until).Minutes()))
		return ctx.Tf("notice.loginpaused", minutes), http.StatusTooManyRequests
	}
	var refused AuthError
	if errors.As(err, &refused) {
		return ctx.T("notice.loginfailed"), http.StatusUnauthorized
	}
	var domain UnknownDomainError
	if errors.As(err, &domain) {
		// Which domain, and in the reader's own language: the error's
		// own words are English and are for the log.
		if domain.Domain == "" {
			return ctx.T("login.needsdomain"), http.StatusUnauthorized
		}
		return fmt.Sprintf(ctx.T("login.baddomain"), domain.Domain), http.StatusUnauthorized
	}
	var baseline BaselineError
	if errors.As(err, &baseline) {
		return fmt.Sprintf(ctx.T("notice.loginerror"), baseline.Error()), http.StatusBadGateway
	}
	var dial *net.OpError
	if errors.As(err, &dial) {
		return fmt.Sprintf(ctx.T("notice.loginerror"), dial.Err), http.StatusServiceUnavailable
	}
	var upstream UpstreamError
	if errors.As(err, &upstream) {
		return fmt.Sprintf(ctx.T("notice.loginerror"), upstream.Error()), http.StatusGatewayTimeout
	}
	return "", http.StatusInternalServerError
}

// recentlyFailed reports whether signing this account in was tried and
// failed too recently to be worth trying again.
func (s *Server) recentlyFailed(username string) bool {
	when, ok := s.loginFailures.Load(username)
	if !ok {
		return false
	}
	if time.Since(when.(time.Time)) < loginRetryAfter {
		return true
	}
	s.loginFailures.CompareAndDelete(username, when)
	return false
}

// loginFailed records a failed automatic sign-in and lets go of every
// failure past its retry window, so addresses that never sign in again
// are not kept for good.
func (s *Server) loginFailed(username string) {
	now := time.Now()
	s.loginFailures.Range(func(k, when any) bool {
		if now.Sub(when.(time.Time)) >= loginRetryAfter {
			s.loginFailures.CompareAndDelete(k, when)
		}
		return true
	})
	s.loginFailures.Store(username, now)
}

// RestoreRememberedAccounts signs every account this session remembers
// back in, and reports whether any came back. The passwords are the
// session's, opened with the secret the browser carries.
func (ctx *Context) RestoreRememberedAccounts() bool {
	session := ctx.lookupSession()
	if session == nil {
		return false
	}
	type credential struct{ address, password string }
	var want []credential
	session.mu.Lock()
	for address, sealed := range session.remember {
		password, ok := ctx.unseal(sealed)
		if !ok {
			continue
		}
		want = append(want, credential{address, password})
	}
	session.mu.Unlock()

	// The accounts sign in concurrently: one unreachable upstream must
	// not add its timeout to the others' wait.
	logins := make([]*Login, len(want))
	refused := make([]error, len(want))
	var wg sync.WaitGroup
	for i, c := range want {
		if ctx.Server.recentlyFailed(c.address) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := ctx.Server.Logins.Put(c.address, c.password, ctx.RealIP())
			if err != nil {
				ctx.Server.loginFailed(c.address)
				ctx.Logger().Printf("Login failed for %q: %v", c.address, err)
				refused[i] = err
				return
			}
			ctx.Server.loginFailures.Delete(c.address)
			logins[i] = s
		}()
	}
	wg.Wait()

	// An account missing from the rail is otherwise a silent loss: the
	// reader is owed its name and the reason it did not come back.
	var lost, marked []string
	forgot := false
	for i, err := range refused {
		if err == nil {
			continue
		}
		// A password the mail server refuses is not tried again: every
		// retry is a failed login from the reader's own address, which
		// a server counting them bans.
		if errors.As(err, new(AuthError)) {
			session.forget(want[i].address)
			forgot = true
		}
		why, _ := ctx.SignInRefusal(err)
		if why == "" {
			why = err.Error()
		}
		say := ctx.T("notice.restorefailed")
		lost = append(lost, fmt.Sprintf(say, want[i].address, why))
		// The address reads left to right wherever the sentence runs,
		// and the server's own words carry their own direction.
		marked = append(marked, fmt.Sprintf(say,
			`<bdi class="ltr">`+template.HTMLEscapeString(want[i].address)+`</bdi>`,
			`<bdi>`+template.HTMLEscapeString(why)+`</bdi>`))
	}
	if forgot {
		// The session stays, for the notice it owes; only what was kept
		// of it goes when no password is left.
		var err error
		if session.remembered() {
			err = ctx.Server.Sessions.Save(session)
		} else if ctx.Server.Sessions.Remembers() {
			err = ctx.Server.Sessions.Records().Delete(session.ID)
		}
		if err != nil {
			ctx.Logger().Printf("failed to write the session: %v", err)
		}
	}
	if len(lost) > 0 {
		session.Notify(Notice{
			Kind:   NoticeFailed,
			Text:   strings.Join(lost, " · "),
			Markup: template.HTML(strings.Join(marked, " · ")),
		})
	}

	restored := false
	for _, s := range logins {
		if s == nil {
			continue
		}
		session.Add(s)
		ctx.accountsLoaded = false
		ctx.Login = s
		ctx.DefaultLogin = s
		for _, ready := range ctx.Server.OnAccountReady {
			ready(ctx, s)
		}
		restored = true
	}
	if restored {
		live, _ := session.Accounts()
		ctx.Login, ctx.DefaultLogin = live[0], live[0]
	}
	return restored
}
