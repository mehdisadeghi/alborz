package dav

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path"

	"github.com/emersion/go-webdav"
)

// delta is what a sync learned of a collection.
type delta struct {
	token, ctag string
	noSync      bool
	// whole says put holds every object there is.
	whole bool
	put   []object
	gone  []string
}

// sent is an object at path as the server sent it; data is nil where the server left it out.
type sent struct {
	path, etag string
	data       []byte
}

// synced is one sync-collection answer; truncated says the token continues it (RFC 6578 3.6).
type synced struct {
	token     string
	truncated bool
	put       []sent
	gone      []string
}

// pull syncs from the held token (RFC 6578), from scratch when refused, or by etags without sync.
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

// refused is a server declining rather than failing: a stale token (RFC 6578 3.2) or an unknown report.
func refused(err error) bool {
	code, _ := webdav.HTTPErrorCode(err)
	return code >= 400 && code < 500 || code == http.StatusNotImplemented
}

func (w wire) syncCollection(ctx context.Context, client *http.Client, target *url.URL, token string) (delta, error) {
	d := delta{whole: token == ""}
	var missing []string
	for {
		s, err := w.sync(ctx, client, target, token)
		if err != nil {
			return delta{}, err
		}
		if s.token == "" {
			return delta{}, webdav.NewHTTPError(http.StatusNotImplemented, errors.New("dav: a sync answered with no sync-token"))
		}
		for _, p := range s.gone {
			if name := nameIn(p, target.Path); name != "" {
				d.gone = append(d.gone, name)
			}
		}
		for _, o := range s.put {
			switch name := nameIn(o.path, target.Path); {
			case name == "":
			case o.data == nil:
				missing = append(missing, o.path)
			default:
				d.put = append(d.put, object{Name: name, ETag: o.etag, Data: o.data})
			}
		}
		token = s.token
		if !s.truncated {
			break
		}
	}
	d.token = token
	fetched, err := w.multiget(ctx, client, target, missing)
	d.put = append(d.put, fetched...)
	return d, err
}

// compare fetches what differs by etag; nothing while the ctag stands.
func (w wire) compare(ctx context.Context, client *http.Client, target *url.URL, h mirrored, have func() (map[string]string, error)) (delta, error) {
	ctag, err := w.ctag(ctx, client, target)
	if err != nil {
		return delta{}, err
	}
	d := delta{noSync: true, ctag: ctag}
	if ctag != "" && ctag == h.CTag {
		return d, nil
	}
	c, err := webdav.NewClient(client, target.String())
	if err != nil {
		return delta{}, err
	}
	files, err := c.ReadDir(ctx, target.Path, false)
	if err != nil {
		return delta{}, err
	}
	listed := map[string]string{}
	for _, f := range files {
		if name := nameIn(f.Path, target.Path); name != "" && !f.IsDir {
			listed[name] = f.ETag
		}
	}
	mine, err := have()
	if err != nil {
		return delta{}, err
	}
	var changed []string
	for name, etag := range listed {
		if etag == "" || mine[name] != etag {
			changed = append(changed, target.Path+name)
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

func (w wire) multiget(ctx context.Context, client *http.Client, target *url.URL, paths []string) ([]object, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	got, err := w.get(ctx, client, target, paths)
	if err != nil {
		return nil, err
	}
	var out []object
	for _, o := range got {
		if name := nameIn(o.path, target.Path); name != "" && o.data != nil {
			out = append(out, object{Name: name, ETag: o.etag, Data: o.data})
		}
	}
	return out, nil
}

func nameIn(p, coll string) string {
	dir, name := path.Split(p)
	if dir != coll {
		return ""
	}
	return name
}
