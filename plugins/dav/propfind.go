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

// MultiStatus is a 207 answer; P is the property set the request asked
// for, which is the one thing a calendar and an address book listing
// do not share.
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

// Propfind lists a collection's children with the properties body asks
// for.
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

// DeleteCollection removes a collection and everything in it. WebDAV
// 9.6 knows no other kind of delete: Depth is infinity and there is no
// asking. Whatever guard there is belongs on the page before this.
func DeleteCollection(ctx context.Context, client *http.Client, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("failed to delete the collection: %s", resp.Status)
	}
	return nil
}

func XMLEscape(s string) string {
	var buf bytes.Buffer
	xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// CanonicalCollectionPath normalizes a collection href or stored path so
// identities compare stably regardless of server formatting. The trailing
// slash keeps requests redirect-free and prefix matches collision-safe.
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
