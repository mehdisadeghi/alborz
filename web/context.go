package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"git.mehdix.org/alborz"
)

// Context is one request as a page sees it.
type Context struct {
	Server *Server

	request  *http.Request
	response *Response
	query    url.Values

	// quiet marks an action whose result shows in place, so it writes no notice.
	quiet         bool
	session       *Session
	sessionSecret string
	Login         *alborz.Login
	defaultLogin  *alborz.Login // the first listed account, for a page that needs one and was given none

	// Unified marks the merged all-accounts view; Login then anchors to one of its accounts.
	Unified bool

	// The accounts cookie only reflects the request, so account changes within one handler read their own writes here.
	accounts       []*alborz.Login
	accountsLoaded bool
	lostAccounts   []string

	// A cookie written to the answer is not in the request, so a handler reads its own writes here.
	prefs map[string]string

	timing *alborz.Timing

	urlAccount string

	// form is the multipart form read, whose files go with the request.
	form *Form
}

func (ctx *Context) Request() *http.Request { return ctx.request }

// SetRequest replaces the request, as one carrying a new context.
func (ctx *Context) SetRequest(r *http.Request) {
	ctx.request = r
	ctx.query = nil
}

func (ctx *Context) Response() *Response { return ctx.response }

// Logger is the server's logger.
func (ctx *Context) Logger() *log.Logger { return ctx.Server.Logger() }

// Param is the path value the route names, unescaped.
func (ctx *Context) Param(name string) string { return ctx.request.PathValue(name) }

func (ctx *Context) QueryParam(name string) string { return ctx.QueryParams().Get(name) }

func (ctx *Context) QueryParams() url.Values {
	if ctx.query == nil {
		ctx.query = ctx.request.URL.Query()
	}
	return ctx.query
}

// FormValue is as net/http's, a form that does not parse reading as empty.
func (ctx *Context) FormValue(name string) string {
	ctx.parseForm()
	return ctx.request.FormValue(name)
}

// FormParams are the fields of a form, multipart or not, and the query.
func (ctx *Context) FormParams() (url.Values, error) {
	if err := ctx.parseForm(); err != nil {
		return nil, err
	}
	return ctx.request.Form, nil
}

func (ctx *Context) FormFile(name string) (*File, error) {
	if err := ctx.parseForm(); err != nil {
		return nil, err
	}
	if ctx.form == nil || len(ctx.form.File[name]) == 0 {
		return nil, http.ErrMissingFile
	}
	return ctx.form.File[name][0], nil
}

func (ctx *Context) MultipartForm() (*Form, error) {
	if err := ctx.parseForm(); err != nil {
		return nil, err
	}
	if ctx.form == nil {
		return nil, http.ErrNotMultipart
	}
	return ctx.form, nil
}

// parseForm reads the form once, a multipart one by ReadForm: net/http's own parse would spill its
// files unsealed to disk (ADR 65). The request then answers for its fields as if it had parsed them.
func (ctx *Context) parseForm() error {
	req := ctx.request
	if err := req.ParseForm(); err != nil || req.MultipartForm != nil ||
		!strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/form-data") {
		return err
	}
	reader, err := req.MultipartReader()
	if err != nil {
		return err
	}
	form, err := ReadForm(reader)
	if err != nil {
		return err
	}
	ctx.form = form
	req.PostForm = form.Value
	for k, vs := range form.Value {
		req.Form[k] = append(req.Form[k], vs...)
	}
	req.MultipartForm = &multipart.Form{Value: form.Value}
	return nil
}

func (ctx *Context) Cookie(name string) (*http.Cookie, error) { return ctx.request.Cookie(name) }

func (ctx *Context) SetCookie(c *http.Cookie) { http.SetCookie(ctx.response, c) }

// Scheme is https behind TLS or a trusted proxy that says so, else http.
func (ctx *Context) Scheme() string {
	h := ctx.request.Header
	switch {
	case ctx.request.TLS != nil:
		return "https"
	case !ctx.viaTrustedProxy():
		return "http"
	case h.Get("X-Forwarded-Proto") != "":
		return h.Get("X-Forwarded-Proto")
	case h.Get("X-Forwarded-Protocol") != "":
		return h.Get("X-Forwarded-Protocol")
	case h.Get("X-Forwarded-Ssl") == "on":
		return "https"
	case h.Get("X-Url-Scheme") != "":
		return h.Get("X-Url-Scheme")
	}
	return "http"
}

// RealIP is the reader's address: the connection's, or the nearest one a trusted proxy names.
func (ctx *Context) RealIP() string {
	direct, _, _ := net.SplitHostPort(ctx.request.RemoteAddr)
	forwarded := ctx.request.Header.Values("X-Forwarded-For")
	if len(ctx.Server.trusted) == 0 || len(forwarded) == 0 {
		return direct
	}
	hops := append(strings.Split(strings.Join(forwarded, ","), ","), direct)
	for i := len(hops) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(hops[i]), "[]"))
		if err != nil {
			return direct
		}
		if !ctx.Server.trusts(ip) {
			return ip.Unmap().String()
		}
	}
	// Every hop is trusted, so the farthest is the best guess.
	return strings.TrimSpace(hops[0])
}

// viaTrustedProxy reports whether the connection comes from a proxy whose forwarded headers are believed.
func (ctx *Context) viaTrustedProxy() bool {
	peer, err := netip.ParseAddrPort(ctx.request.RemoteAddr)
	return err == nil && ctx.Server.trusts(peer.Addr())
}

// Redirect answers with code and the location, and no body.
func (ctx *Context) Redirect(code int, to string) error {
	if code < 300 || code > 308 {
		return NewError(http.StatusInternalServerError, "invalid redirect status")
	}
	ctx.response.Header().Set("Location", to)
	ctx.response.WriteHeader(code)
	return nil
}

// Render writes the named page or partial; nothing is written when it fails.
func (ctx *Context) Render(code int, name string, data any) error {
	var buf bytes.Buffer
	if err := ctx.Server.renderer().Render(&buf, name, data, ctx); err != nil {
		return err
	}
	return ctx.Blob(code, "text/html; charset=UTF-8", buf.Bytes())
}

func (ctx *Context) JSON(code int, v any) error {
	ctx.contentType("application/json")
	ctx.response.WriteHeader(code)
	return json.NewEncoder(ctx.response).Encode(v)
}

func (ctx *Context) NoContent(code int) error {
	ctx.response.WriteHeader(code)
	return nil
}

func (ctx *Context) Blob(code int, contentType string, b []byte) error {
	ctx.contentType(contentType)
	ctx.response.WriteHeader(code)
	_, err := ctx.response.Write(b)
	return err
}

func (ctx *Context) Stream(code int, contentType string, r io.Reader) error {
	ctx.contentType(contentType)
	ctx.response.WriteHeader(code)
	_, err := io.Copy(ctx.response, r)
	return err
}

// A type the handler set first stays.
func (ctx *Context) contentType(t string) {
	if h := ctx.response.Header(); h.Get("Content-Type") == "" {
		h.Set("Content-Type", t)
	}
}

// Response is the answer being written; Before runs its hooks as the header goes out.
type Response struct {
	http.ResponseWriter
	Status    int
	Committed bool
	before    []func()
}

// Before runs f just before the header is written.
func (r *Response) Before(f func()) { r.before = append(r.before, f) }

func (r *Response) WriteHeader(code int) {
	if r.Committed {
		return
	}
	for _, f := range r.before {
		f()
	}
	r.Status, r.Committed = code, true
	r.ResponseWriter.WriteHeader(code)
}

func (r *Response) Write(b []byte) (int, error) {
	if !r.Committed {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

func (r *Response) Flush() { http.NewResponseController(r.ResponseWriter).Flush() }

func (r *Response) Unwrap() http.ResponseWriter { return r.ResponseWriter }
