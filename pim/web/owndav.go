package web

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/web"
)

// Mount opens the collections kept here and serves them to DAV clients on dav, with the page on app where
// an account names its own servers; the providers of each kind share the store it returns.
func Mount(dav, app *web.Group) (*store.Store, error) {
	srv := app.Server()
	db, err := srv.Data(store.Name, store.Format)
	if err != nil {
		return nil, err
	}
	st := store.New(db)
	serveOwn(dav, srv, st)
	app.GET("server", "/settings/servers/"+web.ServerDAV, mail.Scoped(handleServer))
	app.POST("server", "/settings/servers/"+web.ServerDAV, mail.Scoped(handleServer))
	return st, nil
}

// Each check is an IMAP login, and a syncing phone sends its password with every request.
const davPasswordLife = 5 * time.Minute

// davRealm is what a DAV client shows beside its password prompt.
const davRealm = alborz.AppName

// Refusals are cached too, so a phone's stale password does not spend the reader's sign-in tries.
type davPasswords struct {
	mu      sync.Mutex
	salt    [32]byte
	checked map[[sha256.Size]byte]davVerdict
}

type davVerdict struct {
	refused error
	at      time.Time
}

func newDAVPasswords() *davPasswords {
	p := &davPasswords{checked: make(map[[sha256.Size]byte]davVerdict)}
	rand.Read(p.salt[:])
	return p
}

func (p *davPasswords) digest(username, password string) [sha256.Size]byte {
	return sha256.Sum256(slices.Concat(p.salt[:], []byte(username), []byte{0}, []byte(password)))
}

func (p *davPasswords) known(username, password string, now time.Time) (davVerdict, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	was, ok := p.checked[p.digest(username, password)]
	return was, ok && now.Sub(was.at) < davPasswordLife
}

func (p *davPasswords) keep(username, password string, refused error, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	maps.DeleteFunc(p.checked, func(_ [sha256.Size]byte, v davVerdict) bool {
		return now.Sub(v.at) >= davPasswordLife
	})
	p.checked[p.digest(username, password)] = davVerdict{refused, now}
}

// authenticate checks a DAV client's password the way signing in does, sharing its tries.
func (p *davPasswords) authenticate(srv *web.Server, username, password, source string) error {
	username = strings.ToLower(username)
	now := time.Now()
	if was, ok := p.known(username, password, now); ok {
		return was.refused
	}
	err := srv.CheckPassword(username, password, source)
	if err == nil || errors.As(err, new(alborz.AuthError)) {
		p.keep(username, password, err, now)
	}
	return err
}

// serveOwn answers DAV clients by address and password on each request, with no session or cookie.
func serveOwn(g *web.Group, srv *web.Server, st *store.Store) {
	dav, passwords := st.Handler(), newDAVPasswords()
	// Where a client given only the host starts (RFC 6764).
	home := func(ctx *web.Context) error {
		http.Redirect(ctx.Response(), ctx.Request(), store.Prefix+"/", http.StatusPermanentRedirect)
		return nil
	}
	g.Handle("caldav", "/.well-known/caldav", home)
	g.Handle("carddav", "/.well-known/carddav", home)
	// A published calendar needs no credentials.
	g.Handle("published", store.PublicPrefix, func(ctx *web.Context) error {
		st.ServePublic(ctx.Response(), ctx.Request())
		return nil
	})
	g.Handle("collections", store.Prefix+"/", func(ctx *web.Context) error {
		w, r := ctx.Response(), ctx.Request()
		w.Header().Set(alborz.InstanceHeader, srv.Instance)
		username, password, ok := r.BasicAuth()
		if ok {
			err := passwords.authenticate(srv, username, password, ctx.RealIP())
			var upstream alborz.UpstreamError
			if errors.As(err, &upstream) {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return nil
			}
			// Not 401 for a paused reader: a client hearing 401 asks its owner for a new password.
			var paused alborz.PausedError
			if errors.As(err, &paused) {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(time.Until(paused.Until).Seconds()))))
				http.Error(w, err.Error(), http.StatusTooManyRequests)
				return nil
			}
			ok = err == nil
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="`+davRealm+`", charset="UTF-8"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return nil
		}
		dav.ServeHTTP(w, r.WithContext(store.As(r.Context(), username)))
		return nil
	})
}
