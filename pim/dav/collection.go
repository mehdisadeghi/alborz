package dav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/emersion/go-webdav"
)

// Collection is a WebDAV collection: a calendar, a task list or an address book.
type Collection struct {
	Path     string
	Name     string
	Color    string
	Writable bool
	// Components are the iCalendar components a calendar takes (RFC 4791 5.2.3).
	Components []string
	// RScales are the scales a rule may count in (RFC 7529 10): nil when the server does not say, empty for any.
	RScales []string
	// Account owns the collection, set only where accounts are pooled.
	Account string
	// Source is the ID of the source the collection is on (ADR 28).
	Source string
	// Visible is the reader's standing choice; Shown is what this page shows, which "only" narrows.
	Visible, Shown bool
	// Only marks the sole collection a URL is narrowed to, so pressing its link again widens back.
	Only bool
	// Feed is the address a subscribed calendar kept here is filled from, for its owner; empty for anyone else.
	Feed string
	// Here marks a collection kept in this alborz, which can be shared; SharedBy is its other owner.
	Here     bool
	SharedBy string
	// Public marks a calendar kept here that answers at a public address.
	Public bool
	// SharedWith counts the accounts offered the collection that have not refused it.
	SharedWith int
}

// ListCollections reads every home's collections sorted by name; a failing home is its own error (ADR 28).
func ListCollections(ctx context.Context, homes []Home, list func(ctx context.Context, home string) ([]Collection, error)) ([]Collection, []error) {
	var out []Collection
	var failed []error
	for _, r := range Each(ctx, homes, func(ctx context.Context, home Home) ([]Collection, error) {
		return list(ctx, home.Path)
	}) {
		if r.Err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", r.Site.Path, r.Err))
			continue
		}
		for _, c := range r.Value {
			c.Path = CanonicalCollectionPath(c.Path)
			// DAV:displayname is optional, but the rail needs a name.
			if c.Name == "" {
				c.Name = nameFromPath(c.Path)
			}
			c.Source = r.Site.Source.ID
			if ref, home, here := r.Site.Source.held(c.Path); here {
				c.Here = true
				if ref.Owner != home {
					c.SharedBy = ref.Owner
				}
			}
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, failed
}

func nameFromPath(p string) string {
	p = strings.TrimSuffix(p, "/")
	if at := strings.LastIndex(p, "/"); at >= 0 {
		p = p[at+1:]
	}
	if name, err := url.PathUnescape(p); err == nil {
		return name
	}
	return p
}

// DefaultColor is what a new collection's colour field opens on.
const DefaultColor = "#3366cc"

// CountObjects counts a collection's objects with a Depth 1 PROPFIND.
func CountObjects(ctx context.Context, client *http.Client, base *url.URL, coll string) (int, error) {
	c, err := webdav.NewClient(client, base.String())
	if err != nil {
		return 0, err
	}
	files, err := c.ReadDir(ctx, coll, false)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range files {
		if !f.IsDir {
			n++
		}
	}
	return n, nil
}

// SafeObjectName makes a UID a path segment: all but letters, digits and a few marks become dashes.
func SafeObjectName(uid string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.', r == '@':
			return r
		}
		return '-'
	}, uid)
}

// ErrNameTaken refuses a display name the account already has; no client could tell the two apart.
var ErrNameTaken = errors.New("a collection with that name is already there")

// At is the collection at path, or nil.
func At(colls []Collection, path string) *Collection {
	for i := range colls {
		if colls[i].Path == path {
			return &colls[i]
		}
	}
	return nil
}

// Holding is the account's collection an object lies in, or nil.
func Holding(colls []Collection, account, path string) *Collection {
	for i := range colls {
		if colls[i].Account == account && strings.HasPrefix(path, colls[i].Path) {
			return &colls[i]
		}
	}
	return nil
}

// NameTaken says whether name is, but for case and edges, a display name the account has on place.
func NameTaken(name, place string, existing []Collection) bool {
	return slices.ContainsFunc(existing, func(c Collection) bool {
		return c.Source == place && strings.EqualFold(strings.TrimSpace(c.Name), strings.TrimSpace(name))
	})
}
