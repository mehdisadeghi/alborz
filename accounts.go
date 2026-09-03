package alborz

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fernet/fernet-go"
)

// SetSession sets a cookie for the provided session. Passing a nil session
// unsets the cookie.
func (ctx *Context) SetSession(s *Session) {
	if s == nil {
		ctx.SetCookie(ctx.cookie(cookieName, "", 0))
		return
	}
	ctx.setActiveUser(s.username)
	ctx.SetCookie(ctx.cookie(cookieName, s.token, 0))
}

// setActiveUser records the active account's username so the login page can
// re-authenticate the same identity after its session expired. It
// deliberately survives session expiry; only the final logout clears it.
func (ctx *Context) setActiveUser(username string) {
	ctx.SetCookie(ctx.cookie(activeUserCookieName, url.QueryEscape(username), credentialCookieLife))
}

// Account describes one signed-in account for the account switcher.
type Account struct {
	Username string
	Active   bool
}

// accountSessions returns the live sessions listed in the accounts cookie in
// order. The active session is always a member; expired entries are pruned
// and the cookie rewritten.
func (ctx *Context) accountSessions() []*Session {
	if ctx.accountsLoaded {
		return ctx.accounts
	}

	var tokens []string
	if value, ok := ctx.cookieValue(accountsCookieName, func(string) bool { return true }); ok {
		cookie := &http.Cookie{Value: value}
		tokens = strings.Split(cookie.Value, "|")
	}

	var sessions []*Session
	changed := false
	activeListed := false
	for _, token := range tokens {
		s, err := ctx.Server.Sessions.get(token)
		if err != nil {
			changed = true
			continue
		}
		if s == ctx.Session {
			activeListed = true
		}
		sessions = append(sessions, s)
	}
	if ctx.Session != nil && !activeListed {
		sessions = append([]*Session{ctx.Session}, sessions...)
		changed = true
	}
	if changed {
		ctx.setAccountSessions(sessions)
	} else {
		ctx.accounts = sessions
		ctx.accountsLoaded = true
	}
	return sessions
}

func (ctx *Context) setAccountSessions(sessions []*Session) {
	ctx.accounts = sessions
	ctx.accountsLoaded = true

	tokens := make([]string, len(sessions))
	for i, s := range sessions {
		tokens[i] = s.token
	}
	ctx.SetCookie(ctx.cookie(accountsCookieName, strings.Join(tokens, "|"), 0))
}

// Accounts lists the signed-in accounts in switcher order.
func (ctx *Context) Accounts() []Account {
	sessions := ctx.accountSessions()
	accounts := make([]Account, len(sessions))
	for i, s := range sessions {
		accounts[i] = Account{Username: s.username, Active: s == ctx.Session}
	}
	return accounts
}

// Sessions lists the live sessions of every signed-in account, the
// active one included.
func (ctx *Context) Sessions() []*Session {
	return ctx.accountSessions()
}

// AddAccount makes s the active session and appends it to the account list.
// A previous session for the same username is closed and replaced in place.
func (ctx *Context) AddAccount(s *Session) {
	sessions := ctx.accountSessions()
	replaced := false
	for i, other := range sessions {
		if other.username == s.username {
			other.Close()
			sessions[i] = s
			replaced = true
		}
	}
	if !replaced {
		sessions = append(sessions, s)
	}
	ctx.Session = s
	ctx.DefaultSession = s
	ctx.SetSession(s)
	ctx.setAccountSessions(sessions)
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
	return path + "?account=" + AddressParam(ctx.urlAccount)
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

// SessionFor returns the listed account's live session without making it
// active, nil when the account is not signed in.
func (ctx *Context) SessionFor(username string) *Session {
	for _, s := range ctx.accountSessions() {
		if s.username == username {
			return s
		}
	}
	return nil
}

// SwitchAccount makes the listed account with the given username active. It
// reports false when no live session for that username remains.
func (ctx *Context) SwitchAccount(username string) bool {
	s := ctx.SessionFor(username)
	if s == nil {
		return false
	}
	ctx.Session = s
	ctx.DefaultSession = s
	ctx.SetSession(s)
	return true
}

// Logout closes the active session, removes it from the account list, and
// promotes the next remaining account, whose session is returned. A nil
// return means no account is left and the cookies were cleared.
func (ctx *Context) Logout() *Session {
	return ctx.LogoutAccount(ctx.Session.username)
}

// LogoutAccount closes exactly the named account. Logging out a background
// account preserves the current session; logging out the current account
// promotes the first remaining one.
func (ctx *Context) LogoutAccount(username string) *Session {
	var remaining []*Session
	var target *Session
	for _, s := range ctx.accountSessions() {
		if s.username == username {
			target = s
			continue
		}
		remaining = append(remaining, s)
	}
	if target == nil {
		return ctx.DefaultSession
	}
	target.Close()
	ctx.setAccountSessions(remaining)
	ctx.forgetLoginToken(target.username)
	if len(remaining) == 0 {
		ctx.Session = nil
		ctx.DefaultSession = nil
		ctx.SetSession(nil)
		ctx.setActiveUser("")
		return nil
	}
	if ctx.DefaultSession == target || ctx.DefaultSession == nil {
		ctx.DefaultSession = remaining[0]
	}
	ctx.Session = ctx.DefaultSession
	ctx.SetSession(ctx.DefaultSession)
	return ctx.DefaultSession
}

type loginToken struct {
	Username string
	Password string
}

// SetLoginToken remembers the account's credentials for automatic
// re-authentication after session expiry. Re-login of a known account
// updates its entry in place. The cookie spans the site so logging
// out at /logout can forget the account's credentials; the payload
// stays fernet-encrypted with the server's login key.
func (ctx *Context) SetLoginToken(username, password string) {
	tokens := ctx.loginTokens()
	updated := false
	for i := range tokens {
		if tokens[i].Username == username {
			tokens[i].Password = password
			updated = true
		}
	}
	if !updated {
		tokens = append(tokens, loginToken{username, password})
	}
	ctx.storeLoginTokens(tokens)
}

// forgetLoginToken drops the account's remembered credentials, so logging
// out also forgets how to log back in.
func (ctx *Context) forgetLoginToken(username string) {
	tokens := ctx.loginTokens()
	kept := make([]loginToken, 0, len(tokens))
	for _, t := range tokens {
		if t.Username != username {
			kept = append(kept, t)
		}
	}
	if len(kept) < len(tokens) {
		ctx.storeLoginTokens(kept)
	}
}

func (ctx *Context) storeLoginTokens(tokens []loginToken) {
	if len(tokens) == 0 {
		ctx.SetCookie(ctx.cookie(loginTokenCookieName, "", 0))
		return
	}

	payload, err := json.Marshal(tokens)
	if err != nil {
		panic(err) // Should never happen
	}
	fkey := ctx.Server.Options.LoginKey
	if fkey == nil {
		return
	}

	bytes, err := fernet.EncryptAndSign(payload, fkey)
	if err != nil {
		log.Printf("Warning: login token encryption failed: %v", err)
		return
	}

	ctx.SetCookie(ctx.cookie(loginTokenCookieName, string(bytes), credentialCookieLife))
}

func (ctx *Context) loginTokens() []loginToken {
	cookie, err := ctx.Cookie(loginTokenCookieName)
	if err != nil || cookie == nil {
		return nil
	}

	fkey := ctx.Server.Options.LoginKey
	if fkey == nil {
		return nil
	}

	bytes := fernet.VerifyAndDecrypt([]byte(cookie.Value),
		credentialCookieLife, []*fernet.Key{fkey})
	if bytes == nil {
		return nil
	}

	var tokens []loginToken
	if err := json.Unmarshal(bytes, &tokens); err != nil {
		// A cookie from the single-account format; forget it.
		return nil
	}
	return tokens
}

const (
	// loginRetryAfter is how long a failed automatic sign-in is left
	// alone. Without it every request retries every remembered account,
	// so one unreachable server turns a browser reload - or a page's own
	// assets - into a burst of logins, which is what a provider counting
	// them refuses the account for.
	loginRetryAfter = 2 * time.Minute
)

// recentlyFailed reports whether signing this account in was tried and
// failed too recently to be worth trying again.
func (s *Server) recentlyFailed(username string) bool {
	when, ok := s.loginFailures.Load(username)
	if !ok {
		return false
	}
	return time.Since(when.(time.Time)) < loginRetryAfter
}

// RestoreRememberedAccounts signs every remembered account back in,
// leaving the recorded active one active, and reports whether any
// account came back.
func (ctx *Context) RestoreRememberedAccounts() bool {
	// The accounts sign in concurrently: one unreachable upstream must
	// not add its timeout to the others' wait.
	tokens := ctx.loginTokens()
	sessions := make([]*Session, len(tokens))
	var wg sync.WaitGroup
	for i, token := range tokens {
		if ctx.Server.recentlyFailed(token.Username) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := ctx.Server.Sessions.Put(token.Username, token.Password)
			if err != nil {
				ctx.Server.loginFailures.Store(token.Username, time.Now())
				ctx.Logger().Printf("Login failed for %q: %v", token.Username, err)
				return
			}
			ctx.Server.loginFailures.Delete(token.Username)
			sessions[i] = s
		}()
	}
	wg.Wait()

	restored := false
	for _, s := range sessions {
		if s == nil {
			continue
		}
		ctx.AddAccount(s)
		restored = true
	}
	if !restored {
		return false
	}
	if cookie, err := ctx.Cookie(activeUserCookieName); err == nil && cookie != nil {
		if active, err := url.QueryUnescape(cookie.Value); err == nil {
			ctx.SwitchAccount(active)
		}
	}
	return true
}
