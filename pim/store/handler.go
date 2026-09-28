package store

import (
	"errors"
	"net/http"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

var (
	_ caldav.Backend                   = calendars{}
	_ caldav.UpdateBackend             = calendars{}
	_ caldav.ConditionalDeleteBackend  = calendars{}
	_ carddav.Backend                  = books{}
	_ carddav.ConditionalDeleteBackend = books{}
)

// The size an import takes in one file, so what can be imported can be kept.
const maxObject = 16 << 20

// Handler serves the store to a request whose context names the account (see As).
func (s *Store) Handler() http.Handler {
	cal := &caldav.Handler{Backend: calendars{s}, Prefix: Prefix}
	card := &carddav.Handler{Backend: books{s}, Prefix: Prefix}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account := userOf(r.Context())
		at, err := parsePath(r.URL.Path)
		if errors.Is(err, errNoSuchPath) {
			// Root and principal name both homes, which neither protocol's server does alone.
			webdav.ServePrincipal(w, r, &webdav.ServePrincipalOptions{
				CurrentUserPrincipalPath: principalPath(account),
				HomeSets: []webdav.BackendSuppliedHomeSet{
					caldav.NewCalendarHomeSet(HomePath(Calendar, account)),
					carddav.NewAddressBookHomeSet(HomePath(AddressBook, account)),
				},
				Capabilities: []webdav.Capability{caldav.CapabilityCalendar, carddav.CapabilityAddressBook},
			})
			return
		}
		if err != nil {
			serveError(w, err)
			return
		}
		if r.ContentLength > maxObject {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxObject)
		map[Kind]http.Handler{Calendar: cal, AddressBook: card}[at.kind].ServeHTTP(w, r)
	})
}

func serveError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var status statusError
	if errors.As(err, &status) {
		code = status.code
	}
	http.Error(w, err.Error(), code)
}
