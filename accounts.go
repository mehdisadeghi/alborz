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

// byName gives accounts one order everywhere; sign-in order is history, not order.
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

// AddAccount adds the account to this browser's session and makes it the request's own.
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

// URLAccount returns the request's account parameter, "" when none.
func (ctx *Context) URLAccount() string {
	return ctx.urlAccount
}

// readable undoes escapes RFC 3986 does not require in a query.
var readable = strings.NewReplacer("%40", "@", "%2F", "/", "%3A", ":", "%3F", "?", "%2C", ",")

// QueryValue escapes v for a URL query, leaving readable what RFC 3986 allows.
func QueryValue(v string) string {
	return readable.Replace(url.QueryEscape(v))
}

// Query encodes q, keys in order, each value as QueryValue writes it.
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

// AccountPath appends the request's account parameter to path.
func (ctx *Context) AccountPath(path string) string {
	if ctx.urlAccount == "" {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "account=" + QueryValue(ctx.urlAccount)
}

// NextOr returns the local page the form asked to return to, or fallback.
func (ctx *Context) NextOr(fallback string) string {
	if next := localPath(ctx.FormValue("next")); next != "" {
		return next
	}
	return fallback
}

// From returns the list an object's page was opened from, or "".
func (ctx *Context) From() string {
	return localPath(ctx.QueryParam("from"))
}

func localPath(raw string) string {
	if raw == "" {
		return ""
	}
	// Browsers read a backslash as a slash, so "/\\host" leaves the site like "//host".
	u, err := url.Parse(raw)
	if err != nil || u.Host != "" || !strings.HasPrefix(u.Path, "/") ||
		strings.ContainsRune(raw, '\\') {
		return ""
	}
	return u.String()
}

// LoginFor returns the account's live login, or nil.
func (ctx *Context) LoginFor(username string) *Login {
	for _, s := range ctx.accountLogins() {
		if s.username == username {
			return s
		}
	}
	return nil
}

// Logout signs out the request's login and returns the next remaining one, or nil.
func (ctx *Context) Logout() *Login {
	return ctx.LogoutAccount(ctx.Login.username)
}

// LogoutAccount signs out the named account and returns the next remaining one, or nil.
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

// SetLoginToken keeps the account's password sealed under the browser's secret.
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
	// loginRetryAfter keeps one unreachable server from turning every request
	// into a burst of logins that a provider may count against the account.
	loginRetryAfter = 2 * time.Minute
)

// signInTries within signInWindow pause the sign-in form for one reader:
// fail2ban's defaults for maxretry and findtime.
const (
	signInTries  = 5
	signInWindow = 10 * time.Minute
)

// PausedError is a sign-in withheld until Until after signInTries refusals.
type PausedError struct{ Until time.Time }

func (e PausedError) Error() string {
	return fmt.Sprintf("too many refused passwords; paused until %v", e.Until.Format(time.RFC3339))
}

// SignIn opens a login for username, counting refusals per reader at from.
func (s *Server) SignIn(username, password, from string) (*Login, error) {
	var login *Login
	err := s.signInRefused.attempt(readerKey(from, username), func() (err error) {
		login, err = s.Logins.Put(username, password, from)
		return err
	})
	return login, err
}

func readerKey(from, username string) string {
	if requestAddress(from) == "" {
		return from + "\x00" + strings.ToLower(username)
	}
	return from
}

// SignInRefusal returns the reader's message and status for a failed sign-in;
// an empty message means alborz itself failed.
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

// loginFailed also drops expired failures, so the map does not grow for good.
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

// RestoreRememberedAccounts signs the session's remembered accounts back in
// and reports whether any came back.
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

	// Concurrent, so one unreachable upstream does not delay the others.
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

	var lost, marked []string
	forgot := false
	for i, err := range refused {
		if err == nil {
			continue
		}
		// A refused password is not retried: each retry is a failed login a server may ban.
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
		marked = append(marked, fmt.Sprintf(say,
			`<bdi class="ltr">`+template.HTMLEscapeString(want[i].address)+`</bdi>`,
			`<bdi>`+template.HTMLEscapeString(why)+`</bdi>`))
	}
	if forgot {
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
