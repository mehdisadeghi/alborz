package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"text/template/parse"
)

// HandlerFunc serves a request.
type HandlerFunc func(*Context) error

// Middleware wraps a handler, doing its part before or after it.
type Middleware func(HandlerFunc) HandlerFunc

// MaxFormSize bounds a form with no file: several times the longest text one carries, a Sieve script.
const MaxFormSize = 4 << 20

// FormFieldsSize is the room a file's form takes beside it, for the other fields and the part headers.
const FormFieldsSize = 64 << 10

// Group is routes under one path prefix, one name prefix and one middleware stack.
type Group struct {
	s      *Server
	prefix string
	name   string
	mw     []Middleware
}

// Group starts a group at prefix, its routes named name.route, run through mw.
func (s *Server) Group(prefix, name string, mw ...Middleware) *Group {
	return &Group{s: s, prefix: prefix, name: name, mw: mw}
}

// Group nests a group: prefixes and names join, and mw runs inside g's own.
func (g *Group) Group(prefix, name string, mw ...Middleware) *Group {
	return &Group{s: g.s, prefix: g.prefix + prefix, name: joinName(g.name, name), mw: append(slices.Clip(g.mw), mw...)}
}

// Server is the server the group serves on.
func (g *Group) Server() *Server { return g.s }

func joinName(outer, inner string) string {
	if outer == "" || inner == "" {
		return outer + inner
	}
	return outer + "." + inner
}

// GET serves a page at path, http.ServeMux's pattern relative to the group.
func (g *Group) GET(name, path string, h HandlerFunc) {
	g.add(http.MethodGet, name, path, g.bounded(http.MethodGet, MaxFormSize, h, refuseTooLarge))
}

func (g *Group) POST(name, path string, h HandlerFunc) {
	g.add(http.MethodPost, name, path, g.bounded(http.MethodPost, MaxFormSize, h, refuseTooLarge))
}

// Upload takes a form carrying files up to limit; one past it is answered by over, h never seeing its empty fields.
func (g *Group) Upload(name, path string, limit int64, h, over HandlerFunc) {
	g.add(http.MethodPost, name, path, g.bounded(http.MethodPost, limit, h, over))
}

// Stream leaves a body up to limit to the handler, to read as it arrives and meet *http.MaxBytesError where it stops.
func (g *Group) Stream(name, path string, limit int64, h HandlerFunc) {
	g.add(http.MethodPost, name, path, g.bounded(http.MethodPost, limit, h, nil))
}

// Handle serves every method at path with h, which bounds what it reads itself.
func (g *Group) Handle(name, path string, h HandlerFunc) {
	g.add("", name, path, h)
}

func refuseTooLarge(*Context) error { return NewError(http.StatusRequestEntityTooLarge) }

// bounded bounds the body before the handler reads it, so an upload past the limit is refused unread.
// A form is parsed first, since one cut short reads as empty fields; with no over, the handler streams it.
func (g *Group) bounded(method string, limit int64, h, over HandlerFunc) HandlerFunc {
	return func(ctx *Context) error {
		req := ctx.Request()
		req.Body = http.MaxBytesReader(ctx.Response(), req.Body, limit)
		if method != http.MethodPost || over == nil {
			return h(ctx)
		}
		err := req.ParseMultipartForm(formMemory)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return over(ctx)
		} else if err != nil && !errors.Is(err, http.ErrNotMultipart) {
			return NewError(http.StatusBadRequest, err)
		}
		return h(ctx)
	}
}

func (g *Group) chain(h HandlerFunc) HandlerFunc {
	for _, mw := range slices.Backward(g.mw) {
		h = mw(h)
	}
	return h
}

// routes are every pattern served and the patterns by name.
type routes struct {
	mux   *http.ServeMux
	names map[string]string
	all   []Route
	errs  []error
}

// Route is one route served; Method is "" where it serves every method.
type Route struct{ Name, Method, Pattern string }

// Routes lists the routes mounted, in the order they were.
func (s *Server) Routes() []Route { return slices.Clone(s.routes.all) }

// add serves h at method and path; no method serves every method.
func (g *Group) add(method, name, path string, h HandlerFunc) {
	rs := &g.s.routes
	pattern := g.prefix + path
	if name != "" {
		name = joinName(g.name, name)
		if was, ok := rs.names[name]; ok && was != pattern {
			rs.errs = append(rs.errs, fmt.Errorf("route name %q names both %s and %s", name, was, pattern))
		}
		rs.names[name] = pattern
	} else if pattern != "/" {
		rs.errs = append(rs.errs, fmt.Errorf("route %s %s has no name", method, pattern))
	}
	rs.all = append(rs.all, Route{Name: name, Method: method, Pattern: pattern})
	if method != "" {
		pattern = method + " " + pattern
	}
	if err := handle(rs.mux, pattern, g.s.serve(g.chain(h))); err != nil {
		rs.errs = append(rs.errs, err)
	}
}

// handle turns ServeMux's panic on a conflicting pattern into an error.
func handle(mux *http.ServeMux, pattern string, h http.Handler) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("route %s: %v", pattern, v)
		}
	}()
	mux.Handle(pattern, h)
	return nil
}

func (s *Server) serve(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx := &Context{Server: s, request: req, response: &Response{ResponseWriter: w}}
		if s.Options.Debug {
			// Arrivals too, so a request that never finishes names its URI.
			s.log.Printf("-> %s %s", req.Method, req.RequestURI)
			defer func() {
				s.log.Printf("method=%s, uri=%s, status=%d", req.Method, req.RequestURI, ctx.response.Status)
			}()
		}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			// A download that fails midway aborts on purpose.
			if v == http.ErrAbortHandler {
				panic(v)
			}
			stack := make([]byte, 4<<10)
			stack = stack[:runtime.Stack(stack, false)]
			s.fail(ctx, fmt.Errorf("[PANIC RECOVER] %v %s", v, stack))
		}()
		if err := h(ctx); err != nil {
			s.fail(ctx, err)
		}
	}
}

// unrouted answers a path no route serves: 405 where another method is served there, else 404.
func (s *Server) unrouted(ctx *Context) error {
	var allow []string
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		probe := *ctx.Request()
		probe.Method = method
		if _, pattern := s.routes.mux.Handler(&probe); pattern != "/" {
			allow = append(allow, method)
		}
	}
	if len(allow) == 0 {
		return NewError(http.StatusNotFound)
	}
	ctx.Response().Header().Set("Allow", strings.Join(allow, ", "))
	return NewError(http.StatusMethodNotAllowed)
}

// wildcard matches a pattern's {name}, {name...} and {$}.
var wildcard = regexp.MustCompile(`\{[^}]*\}`)

// Path is the route's address with values in its wildcards, each escaped as a path segment.
func (s *Server) Path(name string, values ...any) (string, error) {
	pattern, ok := s.routes.names[name]
	if !ok {
		return "", fmt.Errorf("no route named %q", name)
	}
	if n := len(wildcard.FindAllString(strings.ReplaceAll(pattern, "{$}", ""), -1)); n != len(values) {
		return "", fmt.Errorf("route %q takes %d values, given %d", name, n, len(values))
	}
	i := 0
	return wildcard.ReplaceAllStringFunc(pattern, func(w string) string {
		if w == "{$}" {
			return ""
		}
		v := fmt.Sprint(values[i])
		i++
		if strings.HasSuffix(w, "...}") {
			parts := strings.Split(v, "/")
			for j, p := range parts {
				parts[j] = escapeSegment(p)
			}
			return strings.Join(parts, "/")
		}
		return escapeSegment(v)
	}), nil
}

// A colon escaped too, or a segment after a dynamic prefix reads to the escaper as a scheme: a DAV path's
// marker carries a host's port (ADR 28).
func escapeSegment(s string) string { return strings.ReplaceAll(url.PathEscape(s), ":", "%3A") }

func (s *Server) mustPath(name string, values ...any) string {
	p, err := s.Path(name, values...)
	if err != nil {
		panic(err)
	}
	return p
}

// Path is the route's address with values in its wildcards.
func (ctx *Context) Path(name string, values ...any) string {
	return ctx.Server.mustPath(name, values...)
}

// URL is the route's address in the request's account.
func (ctx *Context) URL(name string, values ...any) string {
	return ctx.AccountPath(ctx.Server.mustPath(name, values...))
}

// templateURL is the url function of the templates: a string as the paths written out were, so it
// passes wherever they did.
func (s *Server) templateURL(name string, values ...any) (string, error) {
	return s.Path(name, values...)
}

// checkURLs finds every url call in the templates naming no route, or given the wrong number of values.
func (s *Server) checkURLs(r *renderer) error {
	var errs []error
	for _, t := range r.templates() {
		if t.Tree == nil {
			continue
		}
		walkCommands(t.Tree.Root, false, func(cmd *parse.CommandNode, piped bool) {
			if id, ok := cmd.Args[0].(*parse.IdentifierNode); !ok || id.Ident != "url" {
				return
			}
			at, _ := t.Tree.ErrorContext(cmd)
			if len(cmd.Args) < 2 {
				errs = append(errs, fmt.Errorf("%s: url wants a route name", at))
				return
			}
			name, ok := cmd.Args[1].(*parse.StringNode)
			if !ok {
				errs = append(errs, fmt.Errorf("%s: url wants a route name written out", at))
				return
			}
			n := len(cmd.Args) - 2
			if piped {
				n++
			}
			if _, err := s.Path(name.Text, make([]any, n)...); err != nil {
				errs = append(errs, fmt.Errorf("%s: %v", at, err))
			}
		})
	}
	return errors.Join(errs...)
}

// walkCommands calls f on every command under n; piped says it takes the value of the one before it.
func walkCommands(n parse.Node, piped bool, f func(*parse.CommandNode, bool)) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			walkCommands(c, false, f)
		}
	case *parse.ActionNode:
		walkCommands(n.Pipe, false, f)
	case *parse.TemplateNode:
		walkCommands(n.Pipe, false, f)
	case *parse.IfNode:
		walkBranch(&n.BranchNode, f)
	case *parse.RangeNode:
		walkBranch(&n.BranchNode, f)
	case *parse.WithNode:
		walkBranch(&n.BranchNode, f)
	case *parse.PipeNode:
		if n == nil {
			return
		}
		for i, c := range n.Cmds {
			walkCommands(c, i > 0, f)
		}
	case *parse.CommandNode:
		if len(n.Args) > 0 {
			f(n, piped)
		}
		for _, a := range n.Args {
			walkCommands(a, false, f)
		}
	}
}

func walkBranch(b *parse.BranchNode, f func(*parse.CommandNode, bool)) {
	walkCommands(b.Pipe, false, f)
	walkCommands(b.List, false, f)
	walkCommands(b.ElseList, false, f)
}
