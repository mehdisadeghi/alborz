package dav

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// MultiStatus is a 207 answer carrying property set P.
type MultiStatus[P any] struct {
	Responses []Response[P] `xml:"response"`
}

type Response[P any] struct {
	Href     string        `xml:"href"`
	PropStat []PropStat[P] `xml:"propstat"`
}

type PropStat[P any] struct {
	Status string `xml:"status"`
	Prop   P      `xml:"prop"`
}

// Propfind lists a collection's children with the properties body asks for.
func Propfind[P any](ctx context.Context, client *http.Client, path string, body string) (*MultiStatus[P], error) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString(body)

	req, err := http.NewRequestWithContext(ctx, "PROPFIND", path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("expected multistatus response, got %s", resp.Status)
	}

	var ms MultiStatus[P]
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, err
	}
	return &ms, nil
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

func XMLEscape(s string) string {
	var buf bytes.Buffer
	xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// CanonicalCollectionPath normalizes a collection href; the trailing slash avoids redirects and prefix collisions.
func CanonicalCollectionPath(href string) string {
	href = strings.TrimSpace(href)
	if u, err := url.Parse(href); err == nil {
		href = u.Path
	}
	if !strings.HasPrefix(href, "/") {
		href = "/" + href
	}
	href = path.Clean(href)
	if href != "/" {
		href += "/"
	}
	return href
}
