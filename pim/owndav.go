package pim

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
	"git.mehdix.org/alborz/settings"
	"github.com/labstack/echo/v4"
)

// Mount opens the collections kept here and serves them to DAV clients, with the page where an
// account names its own servers; the providers of each kind share the store it returns.
func Mount(srv *alborz.Server) (*store.Store, error) {
	db, err := srv.Data(store.Name, store.Format)
	if err != nil {
		return nil, err
	}
	st := store.New(db)
	srv.Pre(serveOwn(srv, st))
	r := srv.Router()
	r.GET("/settings/servers/"+settings.ServerDAV, mail.Scoped(handleServer))
	r.POST("/settings/servers/"+settings.ServerDAV, mail.Scoped(handleServer))
	return st, nil
}

// Each check is an IMAP login, and a syncing phone sends its password with every request.
const davPasswordLife = 5 * time.Minute

// davRealm is what a DAV client shows beside its password prompt.
const davRealm = alborz.AppName

// wellKnown are where a client given only the host starts (RFC 6764).
var wellKnown = []string{"/.well-known/caldav", "/.well-known/carddav"}

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
func (p *davPasswords) authenticate(srv *alborz.Server, username, password, source string) error {
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

// serveOwn answers DAV clients ahead of sessions and cookies, by address and password on each request.
func serveOwn(srv *alborz.Server, st *store.Store) echo.MiddlewareFunc {
	dav, passwords := st.Handler(), newDAVPasswords()
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ectx echo.Context) error {
			w, r := ectx.Response(), ectx.Request()
			for _, start := range wellKnown {
				if r.URL.Path == start {
					http.Redirect(w, r, store.Prefix+"/", http.StatusPermanentRedirect)
					return nil
				}
			}
			// A published calendar needs no credentials.
			if store.IsPublic(r.URL.Path) {
				st.ServePublic(w, r)
				return nil
			}
			if !strings.HasPrefix(r.URL.Path, store.Prefix+"/") {
				return next(ectx)
			}
			w.Header().Set(alborz.InstanceHeader, srv.Instance)
			username, password, ok := r.BasicAuth()
			if ok {
				err := passwords.authenticate(srv, username, password, ectx.RealIP())
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
		}
	}
}
