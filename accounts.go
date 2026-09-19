package alborz

import (
	"errors"
	"fmt"
	"html/template"
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

// accountSessions is the visit's bag: every account this browser has
// signed into. An account whose session has ended is dropped and named
// for the page, rather than left in the list as if it were still here.
func (ctx *Context) accountSessions() []*Session {
	if ctx.accountsLoaded {
		return ctx.accounts
	}
	v := ctx.lookupVisit()
	if v == nil {
		return nil
	}
	sessions, lost := v.Accounts()
	ctx.lostAccounts = append(ctx.lostAccounts, lost...)
	ctx.accounts = sessions
	ctx.accountsLoaded = true
	return sessions
}

// byName puts accounts in one order wherever they are listed, the
// rail and every picker alike. Sign-in order is a history, not an
// order: an account signed in again moved to the end.
func byName(sessions []*Session) {
	slices.SortStableFunc(sessions, func(a, b *Session) int {
		return strings.Compare(strings.ToLower(a.username), strings.ToLower(b.username))
	})
}

// Accounts lists the signed-in accounts in rail order.
func (ctx *Context) Accounts() []Account {
	sessions := ctx.accountSessions()
	accounts := make([]Account, len(sessions))
	for i, s := range sessions {
		accounts[i] = Account{Username: s.username}
	}
	return accounts
}

// Sessions lists the live sessions of every signed-in account.
func (ctx *Context) Sessions() []*Session {
	return ctx.accountSessions()
}

// AddAccount puts the account in this browser's bag and makes it the
// request's own. A session for the same address is replaced.
func (ctx *Context) AddAccount(s *Session) {
	v := ctx.Visit()
	v.Add(s)
	// A browser with no reading settings of its own takes what the
	// account brings, whether it is the first to sign in or joins
	// later. One that has been told what to do is never overruled.
	// Silently: the page size it starts with is not news.
	ctx.adoptReading(s.Username())
	ctx.accountsLoaded = false
	ctx.Session = s
	ctx.DefaultSession = s
	for _, ready := range ctx.Server.OnAccountReady {
		ready(ctx, s)
	}
}

// URLAccount returns the account selected by the request's account
// parameter, "" when none.
func (ctx *Context) URLAccount() string {
	return ctx.urlAccount
}

// AddressParam writes an address into a URL query the way a reader
// would type it. RFC 3986 allows "@" unescaped in a query, and every
// address in alborz's links is one, so escaping it only makes the bar
// unreadable. Everything else is escaped as usual.
func AddressParam(address string) string {
	return strings.ReplaceAll(url.QueryEscape(address), "%40", "@")
}

// AddressQuery encodes a whole query the way AddressParam encodes one
// value: an address in it keeps its at sign, for the same reason.
func AddressQuery(q url.Values) string {
	return strings.ReplaceAll(q.Encode(), "%40", "@")
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
	return path + sep + "account=" + AddressParam(ctx.urlAccount)
}

// NextOr returns the local page a form asked to return to, or fallback
// when the request names none, names another host, or names something
// that is not an absolute path. A setting written from a page belongs
// to that page: the form carries where it was submitted from, and the
// handler comes back to it instead of a fixed landing page.
func (ctx *Context) NextOr(fallback string) string {
	next := ctx.FormValue("next")
	if next == "" {
		return fallback
	}
	// A backslash is read as a slash by browsers, so "/\\host" would
	// leave the site the way "//host" does.
	u, err := url.Parse(next)
	if err != nil || u.Host != "" || !strings.HasPrefix(u.Path, "/") ||
		strings.ContainsRune(next, '\\') {
		return fallback
	}
	return u.String()
}

// SessionFor returns the listed account's live session, nil when the
// account is not signed in.
func (ctx *Context) SessionFor(username string) *Session {
	for _, s := range ctx.accountSessions() {
		if s.username == username {
			return s
		}
	}
	return nil
}

// Logout closes the request's session, removes it from the account
// list, and returns the first remaining account's session. A nil return
// means no account is left and the cookies were cleared.
func (ctx *Context) Logout() *Session {
	return ctx.LogoutAccount(ctx.Session.username)
}

// LogoutAccount closes exactly the named account and takes it out of
// the bag. A nil return means the bag is empty and nobody is signed in.
func (ctx *Context) LogoutAccount(username string) *Session {
	v := ctx.lookupVisit()
	if v == nil {
		return nil
	}
	target := v.Remove(username)
	if target == nil {
		return ctx.DefaultSession
	}
	target.Close()
	ctx.accountsLoaded = false
	ctx.forgetLoginToken(target.username)
	remaining, _ := v.Accounts()
	if len(remaining) == 0 {
		ctx.Session = nil
		ctx.DefaultSession = nil
		return nil
	}
	ctx.DefaultSession = remaining[0]
	ctx.Session = ctx.DefaultSession
	return ctx.DefaultSession
}

// SetLoginToken keeps the account's password with this visit, sealed
// under the browser's own secret, so a session that ends or a restart
// costs a reconnection rather than a login. What the browser holds is
// an id and a key, never a password.
func (ctx *Context) SetLoginToken(username, password string) {
	v := ctx.Visit()
	sealed, err := ctx.seal(password)
	if err != nil {
		ctx.Logger().Printf("failed to keep %q signed in: %v", username, err)
		return
	}
	v.Remember(username, sealed)
	if err := ctx.Server.Visits.Save(v); err != nil {
		ctx.Logger().Printf("failed to write the visit: %v", err)
	}
}

// forgetLoginToken drops the account's password, so signing out is
// signing out.
func (ctx *Context) forgetLoginToken(username string) {
	v := ctx.lookupVisit()
	if v == nil {
		return
	}
	v.forget(username)
	if v.remembered() {
		ctx.Server.Visits.Save(v)
		return
	}
	ctx.Server.Visits.Delete(v.ID)
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

// SignIn opens a session for username as the mail server says, for a
// reader at from. Readers are counted by their address, or behind a
// proxy not named as trusted, where every reader has the proxy's, by
// address and account.
func (s *Server) SignIn(username, password, from string) (*Session, error) {
	var sess *Session
	err := s.signInRefused.attempt(readerKey(from, username), func() (err error) {
		sess, err = s.Sessions.Put(username, password, from)
		return err
	})
	return sess, err
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
	return time.Since(when.(time.Time)) < loginRetryAfter
}

// RestoreRememberedAccounts signs every account this visit remembers
// back in, and reports whether any came back. The passwords are the
// visit's, opened with the secret the browser carries.
func (ctx *Context) RestoreRememberedAccounts() bool {
	v := ctx.lookupVisit()
	if v == nil {
		return false
	}
	type credential struct{ address, password string }
	var want []credential
	v.mu.Lock()
	for address, sealed := range v.remember {
		password, ok := ctx.unseal(sealed)
		if !ok {
			continue
		}
		want = append(want, credential{address, password})
	}
	v.mu.Unlock()

	// The accounts sign in concurrently: one unreachable upstream must
	// not add its timeout to the others' wait.
	sessions := make([]*Session, len(want))
	refused := make([]error, len(want))
	var wg sync.WaitGroup
	for i, c := range want {
		if ctx.Server.recentlyFailed(c.address) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := ctx.Server.Sessions.Put(c.address, c.password, ctx.RealIP())
			if err != nil {
				ctx.Server.loginFailures.Store(c.address, time.Now())
				ctx.Logger().Printf("Login failed for %q: %v", c.address, err)
				refused[i] = err
				return
			}
			ctx.Server.loginFailures.Delete(c.address)
			sessions[i] = s
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
			v.forget(want[i].address)
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
		// The visit stays, for the notice it owes; only what was kept
		// of it goes when no password is left.
		var err error
		if v.remembered() {
			err = ctx.Server.Visits.Save(v)
		} else if ctx.Server.Visits.Remembers() {
			err = ctx.Server.Visits.Keeper().Delete(v.ID)
		}
		if err != nil {
			ctx.Logger().Printf("failed to write the visit: %v", err)
		}
	}
	if len(lost) > 0 {
		v.Notify(Notice{
			Kind:   NoticeFailed,
			Text:   strings.Join(lost, " · "),
			Markup: template.HTML(strings.Join(marked, " · ")),
		})
	}

	restored := false
	for _, s := range sessions {
		if s == nil {
			continue
		}
		v.Add(s)
		ctx.accountsLoaded = false
		ctx.Session = s
		ctx.DefaultSession = s
		for _, ready := range ctx.Server.OnAccountReady {
			ready(ctx, s)
		}
		restored = true
	}
	if restored {
		live, _ := v.Accounts()
		ctx.Session, ctx.DefaultSession = live[0], live[0]
	}
	return restored
}
