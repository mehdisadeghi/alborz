package collections

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-webdav"
	bolt "go.etcd.io/bbolt"
)

// Prefix is where the server is mounted. Long enough that no DAV server
// an account names elsewhere shares it: alborz tells its own collections
// from a remote server's by the path alone.
const Prefix = "/alborz/dav"

// homes is where each kind's collections are listed under an account.
var homes = map[Kind]string{Calendar: "calendars", AddressBook: "addressbooks"}

// aliasSep joins a shared collection's name to its owner in the path an
// invitee reaches it by. A name cannot hold it, so the first one ends
// the name, whatever an address may hold after it.
const aliasSep = "~"

// nameRule is what a collection's or an object's name may be. Clients
// choose them; they become keys and path segments.
var nameRule = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,120}$`)

// named says whether s may name a collection or an object. The rule
// admits the two names a path reads as a step and not a name: joined
// to its collection, ".." is the home.
func named(s string) bool { return nameRule.MatchString(s) && s != "." && s != ".." }

type userKey struct{}

// As is the context of a request made as the account named.
func As(ctx context.Context, account string) context.Context {
	return context.WithValue(ctx, userKey{}, strings.ToLower(account))
}

func userOf(ctx context.Context) string {
	account, _ := ctx.Value(userKey{}).(string)
	return account
}

func principalPath(account string) string {
	return Prefix + "/principals/" + url.PathEscape(account) + "/"
}

// HomePath is where an account's collections of a kind are listed.
func HomePath(kind Kind, account string) string {
	return Prefix + "/" + homes[kind] + "/" + url.PathEscape(account) + "/"
}

// PathOf is the collection's address as one account reaches it: by its
// name in its owner's home, by name and owner in anyone else's.
func PathOf(viewer string, c Collection) string {
	name := c.ID
	if !strings.EqualFold(viewer, c.Owner) {
		name += aliasSep + c.Owner
	}
	return HomePath(c.Kind, strings.ToLower(viewer)) + url.PathEscape(name) + "/"
}

// place is what a path under the prefix names: a home, a collection in
// it, or an object in that. A home's ref names no collection.
type place struct {
	kind   Kind
	home   string // the account whose home the path is in
	ref    Ref
	object string
}

var errNoSuchPath = httpError(http.StatusNotFound, errors.New("no such path"))

func parsePath(p string) (place, error) {
	rest, ok := strings.CutPrefix(p, Prefix+"/")
	if !ok {
		return place{}, errNoSuchPath
	}
	parts := strings.Split(strings.TrimSuffix(rest, "/"), "/")
	var at place
	for kind, segment := range homes {
		if parts[0] == segment {
			at.kind = kind
		}
	}
	if at.kind == "" || len(parts) < 2 || len(parts) > 4 {
		return place{}, errNoSuchPath
	}
	at.home = strings.ToLower(parts[1])
	if len(parts) == 2 {
		return at, nil
	}
	name, owner, shared := strings.Cut(parts[2], aliasSep)
	at.ref = Ref{Owner: at.home, ID: name}
	if shared {
		at.ref.Owner = strings.ToLower(owner)
	}
	if len(parts) == 4 {
		at.object = parts[3]
	}
	if !named(name) || (at.object != "" && !named(at.object)) {
		return place{}, httpError(http.StatusBadRequest, errors.New("names are letters, digits, and . _ @ -"))
	}
	return at, nil
}

// Held reads a path as the pages hold it: the collection it names when
// it is one kept here, and the account whose home it is reached through,
// which is not the owner's when it is shared.
func Held(p string) (ref Ref, home string, ok bool) {
	at, err := parsePath(p)
	return at.ref, at.home, err == nil && at.ref.ID != ""
}

// access is what the account of the request may do with the collection
// a path names: nothing, read it, write its objects, or own it.
type access int

const (
	none access = iota
	read
	write
	own
)

var (
	errForbidden = httpError(http.StatusForbidden, errors.New("not yours to change"))
	errNotFound  = httpError(http.StatusNotFound, ErrNotFound)
)

// reach resolves a path for the request's account and says what it may
// do there. A collection the account cannot see is not found, which is
// what it is to that account.
func (s *Store) reach(ctx context.Context, p string, kind Kind) (place, *Collection, access, error) {
	at, err := parsePath(p)
	if err != nil {
		return at, nil, none, err
	}
	return s.reachAt(ctx, at, kind)
}

// reachAt is reach for a path already read.
func (s *Store) reachAt(ctx context.Context, at place, kind Kind) (place, *Collection, access, error) {
	account := userOf(ctx)
	if at.kind != kind || at.home != account {
		return at, nil, none, errNotFound
	}
	var c *Collection
	var may access
	err := s.db.View(func(tx *bolt.Tx) (err error) {
		c, may, err = allowed(tx, at.ref, account, time.Now())
		return err
	})
	if errors.Is(err, ErrNotFound) || (err == nil && c.Kind != kind) {
		return at, nil, none, errNotFound
	}
	return at, c, may, err
}

// object is the object a path names, as the request's account reads it.
func (s *Store) object(ctx context.Context, p string, kind Kind) (*Object, error) {
	at, _, _, err := s.reach(ctx, p, kind)
	if err != nil {
		return nil, err
	}
	o, err := s.Object(at.ref, at.object)
	return o, storeError(err)
}

// objects are those of the collection a path names.
func (s *Store) objects(ctx context.Context, p string, kind Kind) ([]Object, error) {
	at, _, _, err := s.reach(ctx, p, kind)
	if err != nil {
		return nil, err
	}
	return s.Objects(at.ref)
}

// write keeps an object at a path, octet for octet as it was sent, so
// the tag a PUT answers with names what the client holds (RFC 4791
// 5.3.4, RFC 6352 6.3.2.3); accepts is the collection's own say on what
// it takes.
func (s *Store) write(ctx context.Context, p string, kind Kind, data []byte, ifMatch, ifNoneMatch webdav.ConditionalMatch, accepts func(Collection) error) (*Object, error) {
	at, c, may, err := s.reach(ctx, p, kind)
	if err != nil {
		return nil, err
	}
	if may < write || at.object == "" {
		return nil, errForbidden
	}
	if err := accepts(*c); err != nil {
		return nil, err
	}
	o, err := s.PutObject(at.ref, at.object, data, conditions(ifMatch, ifNoneMatch), userOf(ctx))
	return o, storeError(err)
}

// create makes a collection in the account's own home, under a name of
// its own.
func (s *Store) create(ctx context.Context, p string, c Collection) error {
	at, err := parsePath(p)
	if err != nil {
		return err
	}
	if at.kind != c.Kind || at.home != userOf(ctx) || at.ref.Owner != at.home || at.ref.ID == "" || at.object != "" {
		return errForbidden
	}
	c.ID, c.Owner = at.ref.ID, at.home
	if c.Name == "" {
		c.Name = c.ID
	}
	_, err = s.Create(c)
	return storeError(err)
}

// deadOf are a collection's dead properties as go-webdav reports them.
func deadOf(c Collection) []webdav.DeadProperty {
	out := make([]webdav.DeadProperty, len(c.Dead))
	for i, d := range c.Dead {
		out[i] = webdav.DeadProperty{Name: xml.Name{Space: d.Space, Local: d.Local}, XML: d.XML}
	}
	return out
}

// keepDead drops the dead properties a PROPPATCH removes and keeps the
// ones it sets, each in place of the one of its name.
func keepDead(c *Collection, removed []xml.Name, set []webdav.DeadProperty) {
	gone := slices.Clone(removed)
	for _, d := range set {
		gone = append(gone, d.Name)
	}
	c.Dead = slices.DeleteFunc(c.Dead, func(d Dead) bool {
		return slices.Contains(gone, xml.Name{Space: d.Space, Local: d.Local})
	})
	for _, d := range set {
		c.Dead = append(c.Dead, Dead{Space: d.Name.Space, Local: d.Name.Local, XML: d.XML})
	}
}

// set changes a property a PROPPATCH names; one it does not name is nil
// and stays.
func set(to, from *string) {
	if from != nil {
		*to = *from
	}
}

// update changes a collection's own properties, which is its owner's to
// do.
func (s *Store) update(ctx context.Context, p string, kind Kind, change func(*Collection)) error {
	at, _, may, err := s.reach(ctx, p, kind)
	if err != nil {
		return err
	}
	if may != own || at.object != "" {
		return errForbidden
	}
	return storeError(s.Update(at.ref, change))
}

// remove is DELETE: of an object, by whoever may write; of a collection,
// by its owner, while an invitee stops seeing it, which is all a share
// gives them the right to end.
func (s *Store) remove(ctx context.Context, p string, kind Kind) error {
	at, _, may, err := s.reach(ctx, p, kind)
	switch {
	case err != nil:
		return err
	case at.object == "" && may == own:
		return storeError(s.Delete(at.ref))
	case at.object == "":
		return storeError(s.RemoveShare(at.ref, userOf(ctx)))
	case may < write:
		return errForbidden
	}
	return storeError(s.RemoveObject(at.ref, at.object, userOf(ctx)))
}

// statusError is an answer with its HTTP status. go-webdav keeps the
// type of its own to itself, so the one it made rides inside, where its
// servers find it, and the status rides beside it for this one.
type statusError struct {
	code int
	err  error
}

func (e statusError) Error() string { return e.err.Error() }
func (e statusError) Unwrap() error { return e.err }

func httpError(code int, cause error) error {
	return statusError{code, webdav.NewHTTPError(code, cause)}
}

// storeError says a store's answer in HTTP.
func storeError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return errNotFound
	case errors.Is(err, ErrForbidden):
		return errForbidden
	case errors.Is(err, ErrExists):
		return httpError(http.StatusMethodNotAllowed, err)
	case errors.Is(err, ErrPrecondition):
		return httpError(http.StatusPreconditionFailed, err)
	}
	return err
}
