package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/klauspost/compress/gzhttp"
)

var compress = func() func(http.Handler) http.HandlerFunc {
	// Raster images come compressed; SVG is text.
	compressible := func(ct string) bool {
		return gzhttp.DefaultContentTypeFilter(ct) && (!strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "image/svg"))
	}
	wrap, err := gzhttp.NewWrapper(gzhttp.EnableZstd(false), gzhttp.ContentTypeFilter(compressible))
	if err != nil {
		panic(err)
	}
	return wrap
}()

// Compress compresses the answer, unless its type comes compressed or its route is Verbatim.
func Compress(next HandlerFunc) HandlerFunc {
	return func(ctx *Context) error {
		res := ctx.Response()
		plain := res.ResponseWriter
		// The error page is written after the compressor has closed, a panic's too.
		defer func() { res.ResponseWriter = plain }()
		var err error
		compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			res.ResponseWriter = w
			err = next(ctx)
		})).ServeHTTP(plain, ctx.Request())
		return err
	}
}

var crossOrigin = http.NewCrossOriginProtection()

// SameOrigin refuses a cross-origin POST: Lax cookies still let a same-site sibling post.
func SameOrigin(next HandlerFunc) HandlerFunc {
	return func(ctx *Context) error {
		if err := crossOrigin.Check(ctx.Request()); err != nil {
			http.Error(ctx.Response(), err.Error(), http.StatusForbidden)
			return nil
		}
		return next(ctx)
	}
}

// Secure sets the headers every page carries.
func Secure(next HandlerFunc) HandlerFunc {
	return func(ctx *Context) error {
		h := ctx.Response().Header()
		// `style-src 'unsafe-inline'` is required for e-mails with embedded stylesheets.
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'self'; form-action 'self'; base-uri 'none'")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("X-Content-Type-Options", "nosniff")
		// DNS prefetching has privacy implications
		h.Set("X-DNS-Prefetch-Control", "off")
		// Pages carry no validator, so browsers would reuse stale ones; the asset handler upgrades stamped URLs to immutable.
		h.Set("Cache-Control", "no-cache")
		return next(ctx)
	}
}

// Prepare times the request, keeps what it reads of the account stores, and takes a language the URL gives.
func Prepare(next HandlerFunc) HandlerFunc {
	return func(ctx *Context) error {
		ctx.installTiming()
		ctx.SetRequest(ctx.Request().WithContext(alborz.WithStoreMemo(ctx.Request().Context())))
		// A language in the URL is kept, so the next click does not fall back to the old one.
		if lang := queryLanguage(ctx); lang != "" {
			if err := ctx.SetLanguage(lang); err != nil {
				ctx.Logger().Printf("profile: keeping the language: %v", err)
			}
		}
		return next(ctx)
	}
}

// unaskedHeader marks a request a page makes by itself; sending it can only bring the lock nearer.
const unaskedHeader = "Alborz-Unasked"

// unasked reports a request the page made by itself. Sent to sign in, it would draw the sign-in page
// where its answer goes; it gets nothing, and the reader's next click meets the sign-in instead.
func unasked(ctx *Context) bool { return ctx.Request().Header.Get(unaskedHeader) != "" }

// Activity counts the request as the reader acting, which keeps the session from locking.
func Activity(next HandlerFunc) HandlerFunc {
	return func(ctx *Context) error {
		session := ctx.lookupSession()
		if session == nil {
			return next(ctx)
		}
		// Pages saved offline are read with no server to ask for the passkey.
		if session.locks() {
			ctx.Response().Header().Set("Cache-Control", "no-store")
		}
		now := time.Now()
		// A request the page made by itself is not the reader acting, or an open tab would never lock.
		if !session.Locked(now) && !unasked(ctx) {
			session.act(now)
			if session.note(ctx.Request().UserAgent(), alborz.RequestAddress(ctx.RealIP())) {
				if err := ctx.Server.Sessions.Save(session); err != nil {
					ctx.Logger().Printf("failed to write the session: %v", err)
				}
			}
		}
		return next(ctx)
	}
}

// Lock sends a locked session to unlock.
func Lock(next HandlerFunc) HandlerFunc {
	return func(ctx *Context) error {
		if session := ctx.lookupSession(); session != nil && session.Locked(time.Now()) {
			return redirectToUnlock(ctx)
		}
		return next(ctx)
	}
}

// Accounts signs the request into the browser's accounts, where it has any.
func Accounts(next HandlerFunc) HandlerFunc {
	return func(ctx *Context) error { return ctx.signIn(next, false) }
}

// SignedIn is Accounts for a page that needs an account: remembered ones sign back in, else the reader is sent to sign in.
func SignedIn(next HandlerFunc) HandlerFunc {
	return func(ctx *Context) error { return ctx.signIn(next, true) }
}

// A locked session does not sign its remembered accounts back in for whoever holds the browser.
func (ctx *Context) signIn(next HandlerFunc, required bool) error {
	if session := ctx.lookupSession(); session != nil && session.Locked(time.Now()) {
		return next(ctx)
	}
	var login *alborz.Login
	if live := ctx.accountLogins(); len(live) > 0 {
		login = live[0]
	}

	// An intercepted POST cannot be resumed after login, so remembered accounts sign back in first.
	if login == nil && required {
		if ctx.RestoreRememberedAccounts() {
			login = ctx.Login
			// A restore is not one account timing out, so none is named.
			ctx.lostAccounts = nil
		}
	}
	if login == nil && required {
		return redirectToLogin(ctx)
	}
	if login == nil {
		return next(ctx)
	}
	// Every signed-in account stays alive: the rail lists them all.
	logins := ctx.accountLogins()
	for _, login := range logins {
		login.Ping()
	}
	ctx.Login = logins[0]
	ctx.defaultLogin = logins[0]
	// An account that timed out while others stayed is said once, on the page.
	if len(ctx.lostAccounts) > 0 {
		ctx.Notify(Notice{Kind: NoticeWarning,
			Text: ctx.Tf("notice.accountexpired", len(ctx.lostAccounts), strings.Join(ctx.lostAccounts, ", "))})
	}

	// The URL carries the account (ADR 0001); nothing sticks between requests.
	if acct := ctx.QueryParam("account"); acct != "" {
		login := ctx.LoginFor(acct)
		if login == nil {
			return renderInfo(ctx, http.StatusOK, fmt.Sprintf(ctx.T("account.notsignedin"), acct))
		}
		ctx.Login = login
		ctx.urlAccount = acct
	} else if len(logins) > 1 {
		ctx.Unified = true
	}
	return next(ctx)
}

// Noticed catches a POST answered with a redirect and no notice: a handler that forgot to say what it did.
func Noticed(next HandlerFunc) HandlerFunc {
	return func(ctx *Context) error {
		err := next(ctx)
		status := ctx.Response().Status
		if err != nil || ctx.Request().Method != http.MethodPost || status < 300 || status >= 400 {
			return err
		}
		if ctx.quiet {
			return err
		}
		if session := ctx.lookupSession(); session != nil && session.hasNotice() {
			return err
		}
		ctx.Logger().Printf("POST %s answered %d with no notice", ctx.Request().URL.Path, status)
		if ctx.Server.strictNotices {
			return fmt.Errorf("POST %s answered %d with no notice: say what it did, or ctx.Quiet()",
				ctx.Request().URL.Path, status)
		}
		return err
	}
}
