package store

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
)

// Prefix is where the server is mounted; own collections are told from remote ones by it alone.
const Prefix = "/alborz/dav"

var homes = map[Kind]string{Calendar: "calendars", AddressBook: "addressbooks"}

// A name cannot hold aliasSep, so the first one ends the name.
const aliasSep = "~"

// Names become keys and path segments.
var nameRule = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,120}$`)

// The rule admits "." and "..", which a path reads as steps.
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

// PathOf is the collection's address as one account reaches it.
func PathOf(viewer string, c Collection) string {
	name := c.ID
	if !strings.EqualFold(viewer, c.Owner) {
		name += aliasSep + c.Owner
	}
	return HomePath(c.Kind, strings.ToLower(viewer)) + url.PathEscape(name) + "/"
}

type place struct {
	kind   Kind
	home   string
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

// Held reads a path as the pages hold it: the collection, and the home it is reached through.
func Held(p string) (ref Ref, home string, ok bool) {
	at, err := parsePath(p)
	return at.ref, at.home, err == nil && at.ref.ID != ""
}

// HeldObject reads an object's path as its collection and name.
func HeldObject(p string) (ref Ref, name string, ok bool) {
	at, err := parsePath(p)
	return at.ref, at.object, err == nil && at.object != ""
}

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

// A collection the account cannot see is not found.
func (s *Store) reach(ctx context.Context, p string, kind Kind) (place, *Collection, access, error) {
	at, err := parsePath(p)
	if err != nil {
		return at, nil, none, err
	}
	return s.reachAt(ctx, at, kind)
}

func (s *Store) reachAt(ctx context.Context, at place, kind Kind) (place, *Collection, access, error) {
	account := userOf(ctx)
	if at.kind != kind || at.home != account {
		return at, nil, none, errNotFound
	}
	c, may, err := s.may(at.ref, account, time.Now())
	if errors.Is(err, ErrNotFound) || (err == nil && c.Kind != kind) {
		return at, nil, none, errNotFound
	}
	return at, c, may, err
}

func (s *Store) object(ctx context.Context, p string, kind Kind) (*Object, error) {
	at, _, _, err := s.reach(ctx, p, kind)
	if err != nil {
		return nil, err
	}
	o, err := s.Object(at.ref, at.object)
	return o, storeError(err)
}

func (s *Store) objects(ctx context.Context, p string, kind Kind) ([]Object, error) {
	at, _, _, err := s.reach(ctx, p, kind)
	if err != nil {
		return nil, err
	}
	return s.Objects(at.ref)
}

// Kept octet for octet, so the tag a PUT answers names what the client holds (RFC 4791 5.3.4).
func (s *Store) write(ctx context.Context, p string, kind Kind, uid string, data []byte, ifMatch, ifNoneMatch webdav.ConditionalMatch, accepts func(Collection) error) (*Object, error) {
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
	o, err := s.PutObject(at.ref, at.object, uid, data, conditions(ifMatch, ifNoneMatch), userOf(ctx))
	if errors.Is(err, ErrUIDConflict) {
		return nil, err
	}
	return o, storeError(err)
}

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

func deadOf(c Collection) []webdav.DeadProperty {
	out := make([]webdav.DeadProperty, len(c.Dead))
	for i, d := range c.Dead {
		out[i] = webdav.DeadProperty{Name: xml.Name{Space: d.Space, Local: d.Local}, XML: d.XML}
	}
	return out
}

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

func set(to, from *string) {
	if from != nil {
		*to = *from
	}
}

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

// An invitee's DELETE only stops seeing it; the share stays marked so the owner sees who left.
func (s *Store) remove(ctx context.Context, p string, kind Kind, ifMatch webdav.ConditionalMatch) error {
	at, _, may, err := s.reach(ctx, p, kind)
	switch {
	case err != nil:
		return err
	case at.object == "" && may == own:
		return storeError(s.Delete(at.ref))
	case at.object == "":
		return storeError(s.Answer(at.ref, userOf(ctx), false))
	case may < write:
		return errForbidden
	}
	return storeError(s.RemoveObject(at.ref, at.object, conditions(ifMatch, ""), userOf(ctx)))
}

// go-webdav keeps its error type internal, so its error rides inside with the status beside it.
type statusError struct {
	code int
	err  error
}

func (e statusError) Error() string { return e.err.Error() }
func (e statusError) Unwrap() error { return e.err }

func httpError(code int, cause error) error {
	return statusError{code, webdav.NewHTTPError(code, cause)}
}

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
