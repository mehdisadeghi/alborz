package dav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"git.mehdix.org/alborz/pim/store"
)

// Privileges is the DAV:current-user-privilege-set; it says whether a collection is writable.
type Privileges []struct {
	Write        *struct{} `xml:"DAV: write"`
	WriteContent *struct{} `xml:"DAV: write-content"`
	Bind         *struct{} `xml:"DAV: bind"`
}

// Writable reports whether a granted privilege allows writing, and whether the server said at all.
func (ps Privileges) Writable() (writable, known bool) {
	for _, p := range ps {
		known = true
		if p.Write != nil || p.WriteContent != nil || p.Bind != nil {
			writable = true
		}
	}
	return writable, known
}

// CollectionProps is a kind's property set as read from a PROPFIND.
type CollectionProps interface {
	// Collection names the resource when it is of the kind asked for.
	Collection() (name, color string, ok bool)
	Privileges() Privileges
}

// Collection is a WebDAV collection: a calendar, a task list or an address book.
type Collection struct {
	Path     string
	Name     string
	Color    string
	Writable bool
	// Components are the iCalendar components a calendar takes (RFC 4791 5.2.3).
	Components []string
	// Account owns the collection, set only where accounts are pooled.
	Account string
	// Visible is the reader's standing choice; Shown is what this page shows, which "only" narrows.
	Visible, Shown bool
	// Only marks the sole collection a URL is narrowed to, so pressing its link again widens back.
	Only bool
	// Address is a subscribed calendar's feed; empty for a server-held collection.
	Address string
	// Here marks a collection kept in this alborz, which can be shared; SharedBy is its other owner.
	Here     bool
	SharedBy string
	// Public marks a calendar kept here that answers at a public address.
	Public bool
	// SharedWith counts the accounts offered the collection that have not refused it.
	SharedWith int
}

// Listed is a collection with the properties it was read from.
type Listed[P CollectionProps] struct {
	Collection
	Props P
}

// ListCollections reads every home's collections sorted by name; a failing home is its own error (ADR 28).
func ListCollections[P CollectionProps](ctx context.Context, client *http.Client, base *url.URL, homes []Home, propfind string) ([]Listed[P], []error) {
	var responses []Response[P]
	var failed []error
	for _, r := range Each(ctx, homes, func(ctx context.Context, home Home) (*MultiStatus[P], error) {
		return Propfind[P](ctx, client, base.ResolveReference(&url.URL{Path: home.Path}).String(), propfind)
	}) {
		if r.Err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", r.Site.Path, r.Err))
			continue
		}
		responses = append(responses, r.Value.Responses...)
	}
	var out []Listed[P]
	for _, resp := range responses {
		for _, ps := range resp.PropStat {
			if !strings.Contains(ps.Status, "200") {
				continue
			}
			name, color, ok := ps.Prop.Collection()
			if !ok {
				continue
			}
			writable, known := ps.Prop.Privileges().Writable()
			listed := Listed[P]{
				Collection: Collection{
					Path:  CanonicalCollectionPath(resp.Href),
					Name:  name,
					Color: strings.TrimSpace(color),
					// Servers that report no privileges get the benefit
					// of the doubt.
					Writable: writable || !known,
				},
				Props: ps.Prop,
			}
			// DAV:displayname is optional, but the rail needs a name.
			if listed.Name == "" {
				listed.Name = nameFromPath(listed.Path)
			}
			if ref, home, here := store.Held(listed.Path); here {
				listed.Here = true
				if ref.Owner != home {
					listed.SharedBy = ref.Owner
				}
			}
			out = append(out, listed)
			break
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

// Past this many taken names, something other than a clash is going on.
const maxCreateAttempts = 20

// CountObjects counts a collection's children with a Depth 1 PROPFIND for resource types only.
func CountObjects(ctx context.Context, client *http.Client, base *url.URL, coll string) (int, error) {
	target := base.ResolveReference(&url.URL{Path: coll}).String()
	ms, err := Propfind[struct{}](ctx, client, target, `<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/></D:prop></D:propfind>`)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, resp := range ms.Responses {
		if CanonicalCollectionPath(resp.Href) != CanonicalCollectionPath(coll) {
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

// Source is the source the collection is on, which its path names (ADR 28).
func (c Collection) Source() string {
	if rest, ok := strings.CutPrefix(c.Path, "/@"); ok {
		id, _, _ := strings.Cut(rest, "/")
		return id
	}
	if strings.HasPrefix(c.Path, store.Prefix+"/") {
		return SourceHere
	}
	return SourceDomain
}

// At is the collection at path, or nil.
func At(colls []Collection, path string) *Collection {
	for i := range colls {
		if colls[i].Path == path {
			return &colls[i]
		}
	}
	return nil
}

// Holding is the account's collection an object lies in, or nil; a feed's object matches by address.
func Holding(colls []Collection, account, path string) *Collection {
	for i := range colls {
		if colls[i].Account != account {
			continue
		}
		if strings.HasPrefix(path, colls[i].Path) || (colls[i].Address != "" && path == colls[i].Address) {
			return &colls[i]
		}
	}
	return nil
}

// NameTaken says whether name is, but for case and edges, a display name the account has on place.
func NameTaken(name, place string, existing []Collection) bool {
	for _, c := range existing {
		if c.Source() == place && strings.EqualFold(strings.TrimSpace(c.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// ErrCollectionExists is MKCALENDAR or MKCOL answered 405: the address is taken (RFC 4918 9.3.1).
var ErrCollectionExists = errors.New("a collection of that name is already there")

// MakeCollection sends MKCALENDAR or MKCOL; 201, or 207 from some servers, is success.
func MakeCollection(ctx context.Context, client *http.Client, method, target string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusMultiStatus:
		return nil
	case http.StatusMethodNotAllowed:
		return fmt.Errorf("%s: %w", target, ErrCollectionExists)
	default:
		return fmt.Errorf("failed to create the collection: %s", resp.Status)
	}
}

// CreateCollection makes a collection at the first free address under homeSet, walking past taken ones.
func CreateCollection(ctx context.Context, base *url.URL, homeSet, name, fallback string, make func(ctx context.Context, target string) error) (string, error) {
	segment := url.PathEscape(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", "-")))
	if segment == "" {
		segment = fallback
	}
	home := CanonicalCollectionPath(homeSet)
	for attempt := 1; ; attempt++ {
		try := segment
		if attempt > 1 {
			try = fmt.Sprintf("%s-%d", segment, attempt)
		}
		path := home + try + "/"
		target := base.ResolveReference(&url.URL{Path: path}).String()
		err := make(ctx, target)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, ErrCollectionExists) || attempt == maxCreateAttempts {
			return "", err
		}
	}
}

// Prop names a property by namespace and element, for each kind's vendor colour property.
type Prop struct {
	XMLNS, Name string
}

// Proppatch renames or recolours a collection.
func Proppatch(ctx context.Context, client *http.Client, target, name, color string, colorProp Prop) error {
	var props bytes.Buffer
	if name != "" {
		fmt.Fprintf(&props, "<D:displayname>%s</D:displayname>", XMLEscape(name))
	}
	if color != "" {
		fmt.Fprintf(&props, `<C:%s xmlns:C="%s">%s</C:%s>`, colorProp.Name, colorProp.XMLNS, XMLEscape(color), colorProp.Name)
	}
	if props.Len() == 0 {
		return nil
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	fmt.Fprintf(&buf, `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>%s</D:prop></D:set></D:propertyupdate>`, props.String())

	req, err := http.NewRequestWithContext(ctx, "PROPPATCH", target, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusMultiStatus &&
		resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to change the collection: %s", resp.Status)
	}
	return nil
}
