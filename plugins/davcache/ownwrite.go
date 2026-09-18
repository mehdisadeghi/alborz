package davcache

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// A write used to evict its collection, so the page that followed it
// waited for the whole collection to be read again to show one change:
// on a server that takes a second to list a few hundred tasks, every
// star and every tick cost that second. But alborz knows what it wrote.
// The object it PUT, or the one it deleted, is applied to the answers
// already held, the page is served from them at once, and the
// collection is read again behind it, which settles whatever a patch
// cannot know - a filter the new object does not pass, a property the
// server adds.
//
// What cannot be patched honestly is evicted as before: a PUT the server
// answered without an ETag, since the next edit would send a guess in
// If-Match, and a query over a time range, where a new object may or may
// not belong.

const (
	davNS     = "DAV:"
	caldavNS  = "urn:ietf:params:xml:ns:caldav"
	carddavNS = "urn:ietf:params:xml:ns:carddav"
)

// objectData is how each kind of object travels inside a multistatus.
var objectData = map[string]struct{ ns, element string }{
	"text/calendar": {caldavNS, "calendar-data"},
	"text/vcard":    {carddavNS, "address-data"},
}

// applyWrite applies a confirmed PUT or DELETE of one object to the
// collection's cached answers and reports whether it could; false means
// the caller evicts.
func (u *user) applyWrite(req *http.Request, body []byte, resp *http.Response, replay *http.Client) bool {
	if strings.HasSuffix(req.URL.Path, "/") {
		return false
	}
	var object []byte
	var read *entry
	switch req.Method {
	case http.MethodDelete:
	case http.MethodPut:
		etag := resp.Header.Get("ETag")
		mediaType, _, _ := strings.Cut(req.Header.Get("Content-Type"), ";")
		kind, ok := objectData[strings.TrimSpace(strings.ToLower(mediaType))]
		if etag == "" || !ok {
			return false
		}
		object = objectResponse(req.URL.Path, etag, kind.ns, kind.element, body)
		// A server that names an ETag on a PUT stored the object octet
		// for octet (RFC 4791 5.3.4, RFC 6352 6.3.2.3), so what was sent
		// is what a GET would now answer. Every edit begins with that
		// GET; held, the next star or tick on the same object is the PUT
		// alone.
		now := time.Now()
		read = &entry{
			status: http.StatusOK, body: body, fetched: now, lastUse: now,
			header: http.Header{"Content-Type": {req.Header.Get("Content-Type")}, "Etag": {etag}},
			method: http.MethodGet, url: req.URL, replay: replay,
		}
	default:
		return false
	}

	coll := collectionOf(req.URL.Path)
	u.mu.Lock()
	defer u.mu.Unlock()
	for key, e := range u.entries {
		if collectionOf(e.url.Path) != coll {
			continue
		}
		// The object's own page reads it by a REPORT on its path, and
		// an edit returns to that page: what was written answers it, or
		// the page waits on the server twice to show what alborz sent.
		// Any other read of the object by itself is gone.
		if e.url.Path == req.URL.Path {
			patched, ok := patchMultistatus(e, req.URL.Path, object, body)
			if object == nil || !ok {
				delete(u.entries, key)
				continue
			}
			e.body, e.fetched = patched, time.Time{}
			continue
		}
		// Another object's own read holds that object alone, which this
		// write did not touch. Patched, it grew a response for an object
		// it never asked about; evicted, the next page of that object
		// waited on the server for nothing.
		if !strings.HasSuffix(e.url.Path, "/") {
			continue
		}
		// The collection's own properties hold no object; the ctag
		// among them is stale now, and settled with the rest.
		if e.method == "PROPFIND" && e.depth == "0" {
			e.fetched = time.Time{}
			continue
		}
		patched, ok := patchMultistatus(e, req.URL.Path, object, body)
		if !ok {
			delete(u.entries, key)
			continue
		}
		e.body = patched
		// Due at once: what is held is alborz's own account of the
		// collection until the server's replaces it.
		e.fetched = time.Time{}
	}
	if read != nil {
		u.entries[cacheKey(http.MethodGet, req.URL.Path, "", nil)] = read
	}
	delete(u.ctags, coll)
	u.writes[coll]++
	u.dirty = true
	return true
}

// patchMultistatus removes the object from a cached multistatus, or
// puts the new one in its place. An object the answer did not hold is
// added only to a query over the whole collection for that kind of
// component; anything else reports false.
func patchMultistatus(e *entry, objectPath string, object, written []byte) ([]byte, bool) {
	if e.status != http.StatusMultiStatus {
		return nil, false
	}
	at, end, closing, ok := findResponse(e.body, objectPath)
	if !ok {
		return nil, false
	}
	switch {
	case at >= 0:
		return splice(e.body, at, end, object), true
	case object == nil:
		return e.body, true
	case !holdsWholeCollection(e.reqBody, written):
		return nil, false
	}
	return splice(e.body, closing, closing, object), true
}

// holdsWholeCollection says whether a new object belongs in the answer
// to this request: a collection query with no time range, asking for
// the kind of component that was written.
func holdsWholeCollection(query, written []byte) bool {
	if bytes.Contains(query, []byte("time-range")) {
		return false
	}
	switch {
	case bytes.Contains(query, []byte("addressbook-query")):
		return true
	case bytes.Contains(query, []byte("calendar-query")):
		for _, component := range []string{"VTODO", "VEVENT", "VJOURNAL"} {
			if bytes.Contains(written, []byte("BEGIN:"+component)) {
				return bytes.Contains(query, []byte(component))
			}
		}
	}
	return false
}

func splice(body []byte, from, to int64, with []byte) []byte {
	out := make([]byte, 0, len(body)+len(with))
	out = append(out, body[:from]...)
	out = append(out, with...)
	return append(out, body[to:]...)
}

// findResponse locates the response for one href by its bytes, so the
// rest of the document - the server's prefixes and declarations - is
// left exactly as it came. at is -1 when no response names the object;
// closing is where the multistatus ends, which is where a new one goes.
func findResponse(body []byte, objectPath string) (at, end, closing int64, ok bool) {
	d := xml.NewDecoder(bytes.NewReader(body))
	at, depth := int64(-1), 0
	var start int64
	var href strings.Builder
	inHref, matched := false, false
	for {
		before := d.InputOffset()
		tok, err := d.Token()
		if err == io.EOF {
			return at, end, closing, closing > 0
		}
		if err != nil {
			return 0, 0, 0, false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case depth == 2 && t.Name.Space == davNS && t.Name.Local == "response":
				start, matched = before, false
			case depth == 3 && t.Name.Space == davNS && t.Name.Local == "href":
				inHref = true
				href.Reset()
			}
		case xml.CharData:
			if inHref {
				href.Write(t)
			}
		case xml.EndElement:
			switch {
			case inHref:
				inHref = false
				if u, err := url.Parse(strings.TrimSpace(href.String())); err == nil && u.Path == objectPath {
					matched = true
				}
			case depth == 2 && matched && at < 0:
				at, end = start, d.InputOffset()
			case depth == 1:
				closing = before
			}
			depth--
		}
	}
}

// objectResponse is a response that declares its own namespaces, so it
// reads the same wherever in a server's document it is put.
func objectResponse(objectPath, etag, ns, element string, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString(`<response xmlns="` + davNS + `"><href>`)
	xml.EscapeText(&b, []byte((&url.URL{Path: objectPath}).EscapedPath()))
	b.WriteString(`</href><propstat><prop><getetag>`)
	xml.EscapeText(&b, []byte(etag))
	fmt.Fprintf(&b, `</getetag><%s xmlns="%s">`, element, ns)
	xml.EscapeText(&b, data)
	fmt.Fprintf(&b, `</%s></prop><status>HTTP/1.1 200 OK</status></propstat></response>`, element)
	return b.Bytes()
}

// settleBehind reads the collection again after an own write, off the
// request: the patched answers are served meanwhile.
func (u *user) settleBehind(coll string) {
	if !u.claim(coll) {
		return
	}
	var held []*entry
	u.mu.Lock()
	for _, e := range u.entries {
		if collectionOf(e.url.Path) == coll && e.replay != nil {
			held = append(held, e)
		}
	}
	u.mu.Unlock()
	go func() {
		defer u.release(coll)
		ctx, cancel := context.WithTimeout(context.Background(), refreshBudget)
		defer cancel()
		u.refreshCollection(ctx, coll, held)
	}()
}
