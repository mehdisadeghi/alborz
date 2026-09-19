package collections

import (
	"errors"
	"net/http"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

var (
	_ caldav.Backend       = calendars{}
	_ caldav.UpdateBackend = calendars{}
	_ carddav.Backend      = books{}
)

// maxObject bounds what an account sends: an object, or a report naming
// many. It is the size an import takes in one file, so what can be
// imported can be kept; a contact with a photograph is a few megabytes,
// and the data file never shrinks.
const maxObject = 16 << 20

// Handler serves the store to a request whose context names the account
// (see As). The protocols are go-webdav's; what is alborz's is whose
// collection a path names and what the account may do there, which its
// servers ask of the backends.
func (s *Store) Handler() http.Handler {
	cal := &caldav.Handler{Backend: calendars{s}, Prefix: Prefix}
	card := &carddav.Handler{Backend: books{s}, Prefix: Prefix}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account := userOf(r.Context())
		at, err := parsePath(r.URL.Path)
		if errors.Is(err, errNoSuchPath) {
			// The root and the principal say where the homes are, both
			// of them, which neither protocol's server does alone.
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
		// A body that does not say its length is cut where it passes
		// the bound.
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
