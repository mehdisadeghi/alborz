package web_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestImageReferencesSurviveLoadingImages(t *testing.T) {
	a := Alborz(t)
	uid := a.IMAP.Append(t, "INBOX", "From: <eve@example.org>\r\nTo: <"+User+">\r\nSubject: Images\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: multipart/related; boundary=r\r\n\r\n"+
		"--r\r\nContent-Type: text/html\r\n\r\nMARK<img src=\"cid:logo%40example.org\"><img src=\"http://images.example.org/logo.png\">\r\n"+
		"--r\r\nContent-Type: image/png\r\nContent-ID: <logo@example.org>\r\n\r\nPNG\r\n--r--\r\n")
	c := a.SignIn(t, User)
	for _, allowed := range []string{"", "1"} {
		doc := frameDoc(t, c.Page(t, "mail.message", "INBOX", uid, url.Values{"part": {"1"}, "allow-remote-resources": {allowed}}))
		if !strings.Contains(doc, a.Path("mail.message.raw", "INBOX", uid)) {
			t.Error("percent-encoded Content-ID lost its inline image")
		}
		if got := strings.Contains(doc, a.Path("mail.proxy")+"?"); got != (allowed == "1") {
			t.Errorf("allowed=%q: HTTP image proxied=%v", allowed, got)
		}
	}
}

func TestRemoteImageProxyValidatesTheActualResponse(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aN1sAAAAASUVORK5CYII=")
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/generic":
			w.Header().Set("Content-Type", "application/octet-stream")
		case "/missing":
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusNotFound)
		case "/oversized":
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			w.Write(make([]byte, (5<<20)+1))
			return
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>not an image</html>"))
			return
		}
		w.Write(png)
	}))
	defer upstream.Close()
	a := Alborz(t, Guarded(upstream.Client().Transport))
	c := a.SignIn(t, User)
	for _, path := range []string{"generic", "missing", "oversized", "html"} {
		resp := c.Get(t, "mail.proxy", url.Values{"src": {upstream.URL + "/" + path}})
		if path == "generic" {
			if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" || resp.Text != string(png) {
				t.Errorf("valid PNG with generic MIME: %s %q", resp.Status, resp.Header.Get("Content-Type"))
			}
		} else if resp.StatusCode < 400 {
			t.Errorf("%s served as an image: %s", path, resp.Status)
		}
	}
}
