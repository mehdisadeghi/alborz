package dav

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/alborzmail/go-recur"
	"github.com/emersion/go-ical"
)

// ErrScaleUnlisted refuses a calendar object that brings in a scale its server does not list;
// a client must not store one there (RFC 7529 section 10).
var ErrScaleUnlisted = errors.New("dav: the server does not list the calendar scale")

// scaleGuard stands before every calendar object alborz writes, whoever the writer.
type scaleGuard struct {
	next http.RoundTripper
	// colls is the account's listing, which carries each calendar's supported-rscale-set.
	colls func(req *http.Request) ([]Collection, error)
}

func (g scaleGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPut {
		return g.next.RoundTrip(req)
	}
	if media, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type")); media != ical.MIMEType {
		return g.next.RoundTrip(req)
	}
	req, body, err := buffered(req)
	if err != nil {
		return nil, err
	}
	added, err := scalesIn(body)
	if err != nil {
		return nil, err
	}
	if len(added) == 0 {
		return g.next.RoundTrip(req)
	}
	// A rule another client stored stays storable; only what this write brings in is judged.
	if req.Header.Get("If-None-Match") != string(IfNew) {
		held, err := g.held(req)
		if err != nil {
			return nil, err
		}
		added = slices.DeleteFunc(added, func(s string) bool { return slices.Contains(held, s) })
	}
	if len(added) == 0 {
		return g.next.RoundTrip(req)
	}
	colls, err := g.colls(req)
	if err != nil {
		return nil, err
	}
	var listed []string
	if coll := At(colls, path.Dir(req.URL.Path)+"/"); coll != nil {
		listed = coll.RScales
	}
	for _, s := range added {
		if listed == nil || len(listed) > 0 && !slices.ContainsFunc(listed, func(l string) bool { return strings.EqualFold(l, s) }) {
			return nil, fmt.Errorf("%w: %s", ErrScaleUnlisted, s)
		}
	}
	return g.next.RoundTrip(req)
}

// held are the scales of the object the write replaces; none when nothing is there.
func (g scaleGuard) held(put *http.Request) ([]string, error) {
	req, err := http.NewRequestWithContext(put.Context(), http.MethodGet, put.URL.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dav: reading %s before writing it: %s", put.URL.Path, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return scalesIn(body)
}

// scalesIn are the scales an object's rules count in, upper case.
func scalesIn(raw []byte) ([]string, error) {
	cal, err := ical.NewDecoder(bytes.NewReader(raw)).Decode()
	if err != nil {
		return nil, err
	}
	var scales []string
	for _, comp := range cal.Children {
		for _, prop := range comp.Props[ical.PropRecurrenceRule] {
			// A rule in a scale go-recur does not count still names it.
			rule, _ := recur.Parse(prop.Value)
			if rule.Scale != "" && !slices.Contains(scales, rule.Scale) {
				scales = append(scales, rule.Scale)
			}
		}
	}
	return scales, nil
}
