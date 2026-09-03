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
)

// Privileges is the DAV:current-user-privilege-set every listing asks
// for: it is what says whether a collection can be written to.
type Privileges []struct {
	Write        *struct{} `xml:"DAV: write"`
	WriteContent *struct{} `xml:"DAV: write-content"`
	Bind         *struct{} `xml:"DAV: bind"`
}

// Writable reports whether any granted privilege allows writing, and
// whether the server said anything at all.
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

// Collection is a WebDAV collection as an account has it. A calendar,
// a task list and an address book are each one: the RFCs give them no
// types of their own, only what a collection may hold.
type Collection struct {
	Path     string
	Name     string
	Color    string
	Writable bool
	// Components are the iCalendar components a calendar says it takes
	// (RFC 4791 5.2.3); an address book has none.
	Components []string
	// Account owns the collection, set only where accounts are pooled.
	Account string
	Visible bool
	// Only marks the collection a URL is currently narrowed to, and only
	// when it is the sole one: pressing the same link again is how a
	// reader gets back out of a view they pressed their way into.
	Only bool
}

// Listed is one collection with the properties it was read from, for
// what a kind keeps beyond the common fields.
type Listed[P CollectionProps] struct {
	Collection
	Props P
}

// ListCollections reads the home set's collections in one PROPFIND,
// sorted by name: servers list them in storage order, and a sidebar
// wants a stable one. Found and missing properties come in separate
// propstats; the one naming the resource type carries what was found.
func ListCollections[P CollectionProps](ctx context.Context, client *http.Client, base *url.URL, homeSet, propfind string) ([]Listed[P], error) {
	target := base.ResolveReference(&url.URL{Path: homeSet}).String()
	ms, err := Propfind[P](ctx, client, target, propfind)
	if err != nil {
		return nil, err
	}
	var out []Listed[P]
	for _, resp := range ms.Responses {
		for _, ps := range resp.PropStat {
			if !strings.Contains(ps.Status, "200") {
				continue
			}
			name, color, ok := ps.Prop.Collection()
			if !ok {
				continue
			}
			writable, known := ps.Prop.Privileges().Writable()
			out = append(out, Listed[P]{
				Collection: Collection{
					Path:  CanonicalCollectionPath(resp.Href),
					Name:  name,
					Color: strings.TrimSpace(color),
					// Servers that report no privileges get the benefit
					// of the doubt.
					Writable: writable || !known,
				},
				Props: ps.Prop,
			})
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
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

// Holding is the account's collection an object lies in, or nil: an
// object's path starts with its collection's. A list that pools no
// accounts names none on its collections, and is asked with none.
func Holding(colls []Collection, account, path string) *Collection {
	for i := range colls {
		if colls[i].Account != account {
			continue
		}
		if strings.HasPrefix(path, colls[i].Path) {
			return &colls[i]
		}
	}
	return nil
}

// ErrCollectionExists reports the address as taken: MKCALENDAR or MKCOL
// on a resource that is already there is 405, which is what RFC 4918
// asks and what SabreDAV answers. The form says so rather than
// reporting a bare 405.
var ErrCollectionExists = errors.New("a collection of that name is already there")

// MakeCollection sends the kind's own MKCALENDAR or MKCOL and reads the
// answer. 201 is it; some servers report the created properties in a
// 207 instead, which is equally a success.
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

// CreateCollection puts a new collection under the home set at the
// first free address, made by make. The display name is the user's;
// the path segment only has to be unique and legal in a URL. Two
// collections may share a display name but not an address, and a
// server answers a taken one with 405, so this walks on rather than
// making the reader rename what they meant.
func CreateCollection(ctx context.Context, base *url.URL, homeSet, name, fallback string, make func(ctx context.Context, target string) error) error {
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
		target := base.ResolveReference(&url.URL{Path: home + try + "/"}).String()
		err := make(ctx, target)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrCollectionExists) || attempt == 20 {
			return err
		}
	}
}

// Prop names a property by its namespace and element, for the colour
// each kind keeps under a vendor's name of its own.
type Prop struct {
	XMLNS, Name string
}

// Proppatch renames or recolours a collection: the two properties a
// server lets anyone change. The resource type and the component set
// are protected on every server in use, which is why the create form
// asks for them once and this form does not offer them.
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
