package alborztest

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Client is a browser: it keeps its cookies, and follows a redirect only where a method says.
type Client struct {
	HTTP *http.Client
	app  *App
}

// Response is an answer, its body read.
type Response struct {
	*http.Response
	Text string
}

// File is a file a form sends.
type File struct {
	Field, Name, Type string
	Body              io.Reader
}

// Client is a browser that has signed in to nothing.
func (a *App) Client() *Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	return &Client{app: a, HTTP: &http.Client{Jar: jar, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// SignIn is a browser signed in as user.
func (a *App) SignIn(t *testing.T, user string) *Client {
	t.Helper()
	c := a.Client()
	c.SignIn(t, user)
	return c
}

// SignedIn serves alborz with opts and signs in user.
func SignedIn(t *testing.T, user string, opts ...Option) (*App, *Client) {
	t.Helper()
	a := Alborz(t, opts...)
	return a, a.SignIn(t, user)
}

// SignIn adds user to the browser's accounts, landing where the sign-in leads.
func (c *Client) SignIn(t *testing.T, user string) {
	t.Helper()
	resp := c.Post(t, "auth.login", url.Values{"username": {user}, "password": {Password}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("signing in %s answered %s", user, resp.Status)
	}
	c.Follow(t, resp.Header.Get("Location"))
}

// Do sends req with the browser's cookies.
func (c *Client) Do(t *testing.T, req *http.Request) *Response {
	t.Helper()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	return &Response{Response: resp, Text: string(b)}
}

func (c *Client) send(t *testing.T, method, path, contentType string, body io.Reader) *Response {
	t.Helper()
	req, err := http.NewRequest(method, c.app.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.Do(t, req)
}

// Get asks for the named route.
func (c *Client) Get(t *testing.T, route string, args ...any) *Response {
	t.Helper()
	return c.send(t, http.MethodGet, c.app.Path(route, args...), "", nil)
}

// Page is the named route's page, its redirects followed; an error fails the test.
func (c *Client) Page(t *testing.T, route string, args ...any) string {
	t.Helper()
	return c.Follow(t, c.app.Path(route, args...))
}

// Follow is the page at a path a page gave, its redirects followed; an error fails the test.
func (c *Client) Follow(t *testing.T, path string) string {
	t.Helper()
	for range 10 {
		resp := c.send(t, http.MethodGet, path, "", nil)
		switch {
		case resp.StatusCode >= 500:
			t.Fatalf("GET %s: %s\n%s", path, resp.Status, firstLines(resp.Text))
		case resp.StatusCode >= 400:
			t.Fatalf("GET %s: %s", path, resp.Status)
		case resp.StatusCode >= 300:
			path = resp.Header.Get("Location")
			continue
		}
		return resp.Text
	}
	t.Fatalf("GET %s: too many redirects", path)
	return ""
}

// Post sends form to the named route.
func (c *Client) Post(t *testing.T, route string, form url.Values, args ...any) *Response {
	t.Helper()
	return c.PostTo(t, c.app.Path(route, args...), form)
}

// PostTo sends form to a path a page gave.
func (c *Client) PostTo(t *testing.T, path string, form url.Values) *Response {
	t.Helper()
	return c.send(t, http.MethodPost, path, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
}

// Upload sends form and files to the named route as multipart/form-data.
func (c *Client) Upload(t *testing.T, route string, form url.Values, files []File, args ...any) *Response {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for name, values := range form {
		for _, v := range values {
			if err := w.WriteField(name, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, f := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", multipart.FileContentDisposition(f.Field, f.Name))
		typ := f.Type
		if typ == "" {
			typ = "application/octet-stream"
		}
		h.Set("Content-Type", typ)
		part, err := w.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(part, f.Body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return c.send(t, http.MethodPost, c.app.Path(route, args...), w.FormDataContentType(), &body)
}

// Cookie is the browser's cookie called name, or nil.
func (c *Client) Cookie(name string) *http.Cookie {
	u, err := url.Parse(c.app.URL)
	if err != nil {
		panic(err)
	}
	for _, ck := range c.HTTP.Jar.Cookies(u) {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

func firstLines(s string) string {
	lines := strings.SplitN(s, "\n", 6)
	return strings.Join(lines[:min(5, len(lines))], "\n")
}
