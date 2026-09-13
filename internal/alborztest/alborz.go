// Package alborztest serves alborz, and the servers it talks to, to tests. Each fixture takes the
// test and options, and goes when the test ends.
package alborztest

import (
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/cmd/alborz/app"
	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/web"
)

// The rig's mail domain and accounts; Elsewhere is on a domain of its own.
const (
	Domain    = "test.local"
	User      = "a@test.local"
	User2     = "b@test.local"
	Elsewhere = "c@elsewhere.local"
	Password  = "x"
	loginKey  = "YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA="
)

// App is alborz served as cmd/alborz serves it.
type App struct {
	URL    string
	Server *web.Server
	// IMAP is the mail server of Domain.
	IMAP *IMAPServer
	stop func()
}

type config struct {
	options   app.Options
	imap      *IMAPServer
	upstreams []string
	selfDAV   bool
	segments  []web.Segment
}

type Option func(*config)

// WithIMAP serves Domain's mail from s; without it the domain has an IMAP of its own.
func WithIMAP(s *IMAPServer) Option { return func(c *config) { c.imap = s } }

func WithSMTP(s *SMTPServer) Option { return Upstream(Domain, s.Upstream()) }

func WithSieve(s *SieveServer) Option { return Upstream(Domain, s.Upstream()) }

// WithDAV serves Domain's calendars and address books from d.
func WithDAV(d *DAVServer) Option {
	return func(c *config) {
		for _, u := range d.Upstreams() {
			Upstream(Domain, u)(c)
		}
	}
}

// WithDAVHere names alborz itself as Domain's DAV server.
func WithDAVHere() Option { return func(c *config) { c.selfDAV = true } }

// Upstream serves domain from the server at u.
func Upstream(domain, u string) Option {
	return func(c *config) { c.upstreams = append(c.upstreams, domain+"="+u) }
}

// NoCache keeps nothing the servers answered, as -cache=false; without it the copies go in a
// directory of the test's, as cmd/alborz keeps them in the user cache directory.
func NoCache() Option { return func(c *config) { c.options.Cache = false } }

func PublicURL(u string) Option { return func(c *config) { c.options.PublicURL = u } }

// LoginKey seals what alborz keeps under key instead of the rig's.
func LoginKey(key string) Option { return func(c *config) { c.options.LoginKey = key } }

// DataDir keeps alborz.db in dir, for an alborz that starts again over it.
func DataDir(dir string) Option { return func(c *config) { c.options.DataDir = dir } }

func TrustedProxies(proxies ...string) Option {
	return func(c *config) { c.options.TrustedProxies = proxies }
}

// Segments adds segments to the status bar, after those alborz mounts.
func Segments(segments ...web.Segment) Option {
	return func(c *config) { c.segments = append(c.segments, segments...) }
}

// LoginIdle is how long a login outlives its last request.
func LoginIdle(d time.Duration) Option { return func(c *config) { c.options.LoginIdle = d } }

// ReminderTick paces the round that fires event reminders.
func ReminderTick(d time.Duration) Option { return func(c *config) { c.options.ReminderTick = d } }

// MOTD offers the message of the day from the collections in dir.
func MOTD(dir string) Option { return func(c *config) { c.options.MOTD = dir } }

// Guarded carries the requests to servers outsiders name.
func Guarded(rt http.RoundTripper) Option { return func(c *config) { c.options.Guarded = rt } }

// Alborz serves alborz until the test ends.
func Alborz(t *testing.T, opts ...Option) *App {
	t.Helper()
	c := config{options: app.Options{Options: web.Options{
		Options: alborz.Options{LoginKey: loginKey, Cache: true},
		// An action that sends the reader elsewhere says what it did.
		StrictNotices: true,
	}}}
	for _, o := range opts {
		o(&c)
	}
	if c.imap == nil {
		c.imap = IMAP(t)
	}
	if c.options.DataDir == "" {
		c.options.DataDir = t.TempDir()
	}
	if c.options.Cache {
		c.options.CacheDir = t.TempDir()
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c.options.Upstreams = append([]string{Domain + "=" + c.imap.Upstream()}, c.upstreams...)
	if c.selfDAV {
		c.options.Upstreams = append(c.options.Upstreams, Domain+"=caldav+insecure://"+ln.Addr().String()+store.Prefix+"/")
	}
	s, h, err := app.New(log.New(os.Stderr, "", log.LstdFlags), c.options)
	if err != nil {
		t.Fatalf("start alborz: %v", err)
	}
	s.Status = append(s.Status, c.segments...)
	server := &http.Server{Handler: h}
	server.RegisterOnShutdown(s.Changes.Close)
	go server.Serve(ln)
	a := &App{URL: "http://" + ln.Addr().String(), Server: s, IMAP: c.imap}
	a.stop = sync.OnceFunc(func() {
		server.Close()
		s.Close()
	})
	t.Cleanup(a.stop)
	return a
}

// Close stops alborz before the test ends.
func (a *App) Close() { a.stop() }

// Path is the address of the named route with its values; a url.Values among them is the query.
func (a *App) Path(route string, args ...any) string {
	var values []any
	query := url.Values{}
	for _, v := range args {
		if q, ok := v.(url.Values); ok {
			for k, vs := range q {
				query[k] = append(query[k], vs...)
			}
			continue
		}
		values = append(values, v)
	}
	p, err := a.Server.Path(route, values...)
	if err != nil {
		panic(err)
	}
	if len(query) > 0 {
		p += "?" + web.Query(query)
	}
	return p
}

// Pattern is a regular expression for the named route's paths, its wildcards matching wildcards.
func (a *App) Pattern(route string, wildcards ...string) string {
	p, err := a.Server.PathPattern(route, wildcards...)
	if err != nil {
		panic(err)
	}
	return p
}

// Here begins the paths pages hold for what is kept on an alborz with no public URL (ADR 28).
var Here = func() string {
	host, err := os.Hostname()
	if err != nil {
		panic(err)
	}
	return "/@" + strings.ToLower(host)
}()

// At begins the paths pages hold for what is kept on the server at u (ADR 28).
func At(u string) string { return "/@" + strings.TrimPrefix(u, "http://") }

// DAVUpstream names a as the DAV server of Domain.
func (a *App) DAVUpstream() Option {
	return Upstream(Domain, "caldav+insecure://"+strings.TrimPrefix(a.URL, "http://")+a.Path("dav.collections"))
}
