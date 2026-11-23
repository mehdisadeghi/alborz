package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
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
	st := store.New(db, srv.LoginKey, srv.Keys)
	srv.Sealers = append(srv.Sealers, alborz.Sealer{Reseal: st.Reseal, Drop: st.Drop})
	srv.Archive = append(srv.Archive, alborz.ArchivePart{Dir: "collections",
		Export: func(_ context.Context, l *alborz.Login, put func(string, []byte) error) error {
			return st.Export(l.Username(), put)
		},
		Import: func(_ context.Context, l *alborz.Login, files map[string][]byte) ([]alborz.ImportNote, error) {
			unpublished, err := st.Import(l.Username(), files)
			var notes []alborz.ImportNote
			for _, name := range unpublished {
				notes = append(notes, alborz.ImportNote{Key: "data.unpublished", Name: name})
			}
			if errors.Is(err, store.ErrArchive) {
				err = fmt.Errorf("%w: %w", alborz.ErrNotArchive, err)
			}
			return notes, err
		},
	})
	serveOwn(dav, srv, st)
	srv.DeviceChoices, srv.CollectionKey = deviceChoices(st), st.Key
	app.GET("server", "/settings/servers/"+web.ServerDAV, web.Scoped(handleServer))
	app.POST("server", "/settings/servers/"+web.ServerDAV, web.Scoped(handleServer))
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

// davAccess is what a DAV client's password gives: a device password, maybe for reading only or limited to
// the collections of limit and, where keys is not nil, opened with keys alone (ADR 109), or the mail
// password.
type davAccess struct {
	device, readOnly, limited bool
	limit                     []string
	keys                      map[string][]byte
}

// authenticate checks a DAV client's password: a device password of the account first, which is never cached
// so a revoked one is refused at once, then the mail password the way signing in does (ADR 107). Both share
// the sign-in tries.
func (p *davPasswords) authenticate(srv *web.Server, username, password, source string) (davAccess, error) {
	username = strings.ToLower(username)
	now := time.Now()
	if was, ok := p.known(username, password, now); ok && was.refused != nil {
		return davAccess{}, was.refused
	}
	device, err := srv.CheckDevicePassword(username, password, source, davPasswordLife)
	switch {
	case err == nil:
		return davAccess{device: true, readOnly: device.ReadOnly, limited: device.Scoped, limit: limitOf(device.Scope),
			keys: device.Keys(password)}, nil
	case errors.As(err, new(alborz.AuthError)):
		p.keep(username, password, err, now)
		return davAccess{}, err
	case !errors.Is(err, alborz.ErrNoDevicePassword):
		return davAccess{}, err
	}
	if _, ok := p.known(username, password, now); ok {
		return davAccess{}, nil
	}
	err = srv.CheckPassword(username, password, source)
	if err == nil || errors.As(err, new(alborz.AuthError)) {
		p.keep(username, password, err, now)
	}
	return davAccess{}, err
}

// readMethods are what a read-only device password may ask.
var readMethods = []string{http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND", "REPORT"}

// serveOwn answers DAV clients by address and password on each request, with no session or cookie.
func serveOwn(g *web.Group, srv *web.Server, st *store.Store) {
	dav, passwords := st.Handler(), newDAVPasswords()
	// Where a client given only the host starts (RFC 6764).
	home := func(ctx *web.Context) error {
		http.Redirect(ctx.Response(), ctx.Request(), ctx.Path("dav.collections"), http.StatusPermanentRedirect)
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
		var access davAccess
		if ok {
			var err error
			access, err = passwords.authenticate(srv, username, password, ctx.RealIP())
			if _, ok := errors.AsType[alborz.UpstreamError](err); ok {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return nil
			}
			// Not 401 for a paused reader: a client hearing 401 asks its owner for a new password.
			if paused, ok := errors.AsType[alborz.PausedError](err); ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(time.Until(paused.Until).Seconds()))))
				http.Error(w, err.Error(), http.StatusTooManyRequests)
				return nil
			}
			ok = err == nil
		}
		// A phone syncing without a login in a browser opens the account's key itself (ADR 72); a device
		// password has opened it already, or holds its collections' keys in its place.
		if ok && !access.device {
			if err := srv.Unlock(username, password, davPasswordLife); err != nil {
				srv.Logger().Printf("%s: opening the account's key for a DAV client: %v", username, err)
			}
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="`+davRealm+`", charset="UTF-8"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return nil
		}
		served := dav
		if access.limited {
			r = r.WithContext(store.Limited(r.Context(), access.limit))
		}
		if access.keys != nil {
			served = st.Holding(access.keys).Handler()
		}
		if access.readOnly {
			if !slices.Contains(readMethods, r.Method) {
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return nil
			}
			r = r.WithContext(store.ReadOnly(r.Context()))
		}
		served.ServeHTTP(w, r.WithContext(store.As(r.Context(), username)))
		return nil
	})
}
