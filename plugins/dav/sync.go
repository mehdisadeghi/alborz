package dav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/emersion/go-webdav"
)

// delta is what a sync learned of a collection.
type delta struct {
	token, ctag string
	noSync      bool
	// whole says put is every object there is.
	whole bool
	put   []object
	gone  []string
}

// multistatus is a 207 as a sync reads it; an object's data is under
// the one element of its kind.
type multistatus struct {
	Token     string `xml:"DAV: sync-token"`
	Responses []struct {
		Href     string `xml:"DAV: href"`
		Status   string `xml:"DAV: status"`
		PropStat []struct {
			Status string `xml:"DAV: status"`
			Prop   struct {
				ETag     string `xml:"DAV: getetag"`
				CTag     string `xml:"http://calendarserver.org/ns/ getctag"`
				Calendar string `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
				Card     string `xml:"urn:ietf:params:xml:ns:carddav address-data"`
			} `xml:"DAV: prop"`
		} `xml:"DAV: propstat"`
	} `xml:"DAV: response"`
}

// pull brings a collection's copy in step: by RFC 6578 sync from the
// token held, from scratch when the server no longer takes the token,
// and by comparing etags when the server does not sync at all.
func (w wire) pull(ctx context.Context, client *http.Client, target *url.URL, h mirrored, have func() (map[string]string, error)) (delta, error) {
	if !h.NoSync {
		d, err := w.syncCollection(ctx, client, target, h.Token)
		if refused(err) && h.Token != "" {
			d, err = w.syncCollection(ctx, client, target, "")
		}
		if !refused(err) {
			return d, err
		}
	}
	return w.compare(ctx, client, target, h, have)
}

// refused is a server declining the request, rather than failing at
// it: a token it no longer takes (RFC 6578 3.2), or a report it does
// not know.
func refused(err error) bool {
	code := Answered(err)
	return code >= 400 && code < 500 || code == http.StatusNotImplemented
}

func (w wire) syncCollection(ctx context.Context, client *http.Client, target *url.URL, token string) (delta, error) {
	d := delta{whole: token == ""}
	var missing []string
	for {
		ms, err := call(ctx, client, "REPORT", target, "0", fmt.Sprintf(
			`<D:sync-collection xmlns:D="DAV:" xmlns:K="%s"><D:sync-token>%s</D:sync-token><D:sync-level>1</D:sync-level><D:prop><D:getetag/><K:%s/></D:prop></D:sync-collection>`,
			w.ns, XMLEscape(token), w.data))
		if err != nil {
			return delta{}, err
		}
		if ms.Token == "" {
			return delta{}, webdav.NewHTTPError(http.StatusNotImplemented, errors.New("dav: a sync answered with no sync-token"))
		}
		// A server that answers in parts says so with a 507 on the
		// collection itself, and the token goes on from there (RFC 6578
		// 3.6).
		truncated := false
		for _, r := range ms.Responses {
			name := nameIn(r.Href, target.Path)
			switch {
			case name == "":
				truncated = truncated || strings.Contains(r.Status, " 507") && CanonicalCollectionPath(r.Href) == target.Path
			case strings.Contains(r.Status, " 404"):
				d.gone = append(d.gone, name)
			default:
				for _, ps := range r.PropStat {
					if !strings.Contains(ps.Status, " 200") {
						continue
					}
					data := ps.Prop.Calendar + ps.Prop.Card
					if data == "" {
						missing = append(missing, name)
						continue
					}
					d.put = append(d.put, object{Name: name, ETag: unquote(ps.Prop.ETag), Data: []byte(data)})
				}
			}
		}
		token = ms.Token
		if !truncated {
			break
		}
	}
	d.token = token
	fetched, err := w.multiget(ctx, client, target, missing)
	d.put = append(d.put, fetched...)
	return d, err
}

// compare lists the collection's etags and fetches what differs from
// the copy; nothing, while the collection's ctag stands.
func (w wire) compare(ctx context.Context, client *http.Client, target *url.URL, h mirrored, have func() (map[string]string, error)) (delta, error) {
	ms, err := call(ctx, client, "PROPFIND", target, "1",
		`<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><D:prop><D:getetag/><CS:getctag/></D:prop></D:propfind>`)
	if err != nil {
		return delta{}, err
	}
	d := delta{noSync: true}
	listed := map[string]string{}
	for _, r := range ms.Responses {
		for _, ps := range r.PropStat {
			if !strings.Contains(ps.Status, " 200") {
				continue
			}
			if name := nameIn(r.Href, target.Path); name != "" {
				listed[name] = unquote(ps.Prop.ETag)
			} else if CanonicalCollectionPath(r.Href) == target.Path {
				d.ctag = ps.Prop.CTag
			}
		}
	}
	if d.ctag != "" && d.ctag == h.CTag {
		return d, nil
	}
	mine, err := have()
	if err != nil {
		return delta{}, err
	}
	var changed []string
	for name, etag := range listed {
		if etag == "" || mine[name] != etag {
			changed = append(changed, name)
		}
	}
	for name := range mine {
		if _, ok := listed[name]; !ok {
			d.gone = append(d.gone, name)
		}
	}
	d.put, err = w.multiget(ctx, client, target, changed)
	return d, err
}

func (w wire) multiget(ctx context.Context, client *http.Client, target *url.URL, names []string) ([]object, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var hrefs strings.Builder
	for _, name := range names {
		hrefs.WriteString("<D:href>" + XMLEscape((&url.URL{Path: target.Path + name}).EscapedPath()) + "</D:href>")
	}
	ms, err := call(ctx, client, "REPORT", target, "1", fmt.Sprintf(
		`<K:%s xmlns:D="DAV:" xmlns:K="%s"><D:prop><D:getetag/><K:%s/></D:prop>%s</K:%s>`,
		w.multigetName, w.ns, w.data, hrefs.String(), w.multigetName))
	if err != nil {
		return nil, err
	}
	var out []object
	for _, r := range ms.Responses {
		name := nameIn(r.Href, target.Path)
		for _, ps := range r.PropStat {
			if data := ps.Prop.Calendar + ps.Prop.Card; name != "" && strings.Contains(ps.Status, " 200") && data != "" {
				out = append(out, object{Name: name, ETag: unquote(ps.Prop.ETag), Data: []byte(data)})
			}
		}
	}
	return out, nil
}

func call(ctx context.Context, client *http.Client, method string, target *url.URL, depth, body string) (*multistatus, error) {
	req, err := http.NewRequestWithContext(ctx, method, target.String(), strings.NewReader(xml.Header+body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", depth)
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, webdav.NewHTTPError(resp.StatusCode, fmt.Errorf("dav: %s %s: %s", method, target.Path, resp.Status))
	}
	var ms multistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, err
	}
	return &ms, nil
}

// nameIn is the name of the object href names in coll, empty for
// anything else.
func nameIn(href, coll string) string {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	dir, name := path.Split(u.Path)
	if dir != coll {
		return ""
	}
	return name
}

// unquote is an entity tag as go-webdav hands it out: without quotes.
func unquote(etag string) string {
	if s, err := strconv.Unquote(etag); err == nil {
		return s
	}
	return etag
}
