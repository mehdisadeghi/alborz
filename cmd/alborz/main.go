package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"git.mehdix.org/alborz"
	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/labstack/gommon/log"

	"git.mehdix.org/alborz/filters"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/contacts"
)

var themesPath = "./themes"

// serveProfiles listens only locally: a heap profile holds passwords and mail.
func serveProfiles(addr string) {
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		fmt.Fprintf(os.Stderr, "alborz: -pprof %s: the profiles hold the process's memory; give a loopback address such as localhost:6060\n", addr)
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil {
			fmt.Fprintf(os.Stderr, "alborz: -pprof %s: %v\n", addr, err)
			os.Exit(1)
		}
	}()
}

func main() {
	var (
		addr        string
		profileAddr string
		loginKey    string
		proxies     string
		publicURL   string
		options     alborz.Options
	)
	flag.StringVar(&options.Theme, "theme", alborz.AppName, "theme directory name")
	flag.StringVar(&addr, "addr", ":1323", "listening address")
	flag.StringVar(&profileAddr, "pprof", "",
		"loopback address serving Go's profiles, the goroutine leak profile among them; unset serves none")
	flag.BoolVar(&options.Debug, "debug", false, "enable debug logs")
	flag.BoolVar(&options.PrivateServices, "private-services", false,
		"let an account name a calendar or contacts server on plain HTTP or a private address")
	flag.StringVar(&proxies, "trusted-proxy", "",
		"comma-separated addresses or CIDR ranges of the proxies in front of alborz, whose X-Forwarded-For names the reader; unset takes the connection's address")
	flag.StringVar(&publicURL, "public-url", "",
		"where readers reach alborz, such as https://mail.example.org, for links in mail and addresses shown to other clients (or $LBRZ_PUBLIC_URL); unset uses each request's own")
	flag.StringVar(&loginKey, "login-key", "", "Fernet key sealing what alborz keeps (or $LBRZ_LOGIN_KEY); required")
	flag.BoolVar(&options.Cache, "cache", true,
		"keep what the servers answered, so pages open without asking again; false asks every time and keeps nothing")
	flag.StringVar(&options.DataDir, "data-dir", ".",
		"directory keeping alborz.db: remembered logins and settings, sealed under the login key, and the calendars and address books kept here")
	flag.StringVar(&options.ProjectURL, "project-url", "",
		"where the footer's project name links; unset prints the name alone")

	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `usage: alborz [options...] <upstreams...>

Plain imap[s]://, smtp[s]://, sieve://, https:// or +insecure URLs
configure a single provider accepting logins of any domain. Alternatively,
each argument serves one mail domain and logins are only accepted for the
listed domains: a bare domain uses SRV auto-discovery, and explicit
upstreams are given as repeated domain=url arguments, e.g.:

  alborz example.org example.com=imaps://mail.example.com

`)
		flag.PrintDefaults()
	}

	flag.Parse()
	if profileAddr != "" {
		serveProfiles(profileAddr)
	}

	for _, p := range strings.FieldsFunc(proxies, func(r rune) bool { return r == ',' || r == ' ' }) {
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			var a netip.Addr
			if a, err = netip.ParseAddr(p); err == nil {
				prefix = netip.PrefixFrom(a, a.BitLen())
			}
		}
		if err != nil {
			fmt.Fprintf(flag.CommandLine.Output(), "alborz: invalid -trusted-proxy %q: %v\n", p, err)
			os.Exit(2)
		}
		prefix = prefix.Masked()
		options.TrustedProxies = append(options.TrustedProxies, &net.IPNet{
			IP:   prefix.Addr().AsSlice(),
			Mask: net.CIDRMask(prefix.Bits(), prefix.Addr().BitLen()),
		})
	}

	if publicURL == "" {
		publicURL = os.Getenv("LBRZ_PUBLIC_URL")
	}
	if publicURL != "" {
		u, err := url.Parse(publicURL)
		// alborz answers at the root of its host only.
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.Trim(u.Path, "/") != "" {
			fmt.Fprintf(flag.CommandLine.Output(), "alborz: invalid -public-url %q: want scheme://host\n", publicURL)
			os.Exit(2)
		}
		options.PublicURL = u
	}

	// The environment keeps the key out of the process list.
	if loginKey == "" {
		loginKey = os.Getenv("LBRZ_LOGIN_KEY")
	}

	options.Upstreams = flag.Args()
	if len(options.Upstreams) == 0 {
		fmt.Fprintln(flag.CommandLine.Output(), "alborz: no upstream servers specified")
		flag.Usage()
		os.Exit(2)
	}
	options.ThemesPath = themesPath

	if options.Cache {
		dir, err := os.UserCacheDir()
		if err != nil {
			fmt.Fprintf(flag.CommandLine.Output(), "alborz: no cache directory: %v; set $XDG_CACHE_HOME or run with -cache=false\n", err)
			os.Exit(2)
		}
		options.CacheDir = filepath.Join(dir, alborz.AppName)
	}

	if loginKey == "" {
		fmt.Fprintln(flag.CommandLine.Output(),
			"alborz: no login key: pass -login-key or set $LBRZ_LOGIN_KEY to one made by `go run github.com/fernet/fernet-go/cmd/fernet-keygen`")
		os.Exit(2)
	}
	fernetKey, err := fernet.DecodeKey(loginKey)
	if err != nil {
		fmt.Fprintf(flag.CommandLine.Output(), "alborz: invalid -login-key: %v\n", err)
		os.Exit(2)
	}
	options.LoginKey = fernetKey

	e := echo.New()
	e.HideBanner = true
	if l, ok := e.Logger.(*log.Logger); ok {
		l.SetOutput(os.Stderr)
		l.SetHeader("${time_rfc3339} ${level}")
	}
	s, err := alborz.New(e, &options, mail.TemplateFuncs)
	if err != nil {
		e.Logger.Fatal(err)
	}
	mail.Mount(s)
	kept, err := pim.Mount(s)
	if err != nil {
		e.Logger.Fatal(err)
	}
	// Calendar before contacts, so the servers page lists the kept collections last.
	if err := calendar.Mount(s, kept); err != nil {
		e.Logger.Fatal(err)
	}
	if err := contacts.Mount(s, kept); err != nil {
		e.Logger.Fatal(err)
	}
	filters.Mount(s)
	e.Use(middleware.Recover())
	if options.Debug {
		// Log arrivals too, so a request that never finishes names its URI.
		e.Pre(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				e.Logger.Printf("-> %s %s", c.Request().Method, c.Request().RequestURI)
				return next(c)
			}
		})
		e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
			LogMethod: true, LogURI: true, LogStatus: true,
			LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
				e.Logger.Printf("method=%s, uri=%s, status=%d", v.Method, v.URI, v.Status)
				return nil
			},
		}))
		e.Logger.SetLevel(log.DEBUG)
	}

	// Bind before announcing, so the prologue is true.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		e.Logger.Fatal(err)
	}
	e.Listener = ln
	fmt.Fprint(os.Stdout, alborz.Prologue)

	go func() {
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "alborz: %v\n", err)
			os.Exit(1)
		}
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGINT, syscall.SIGTERM)

	for sig := range sigs {
		if sig == syscall.SIGUSR1 {
			if err := s.Reload(); err != nil {
				e.Logger.Errorf("Failed to reload server: %v", err)
			}
		} else {
			break
		}
	}

	fmt.Fprintln(os.Stderr, "alborz: shutting down (up to 5s for active requests; interrupt again to force)")
	ctx, cancel := context.WithDeadline(context.Background(),
		time.Now().Add(5*time.Second))
	go func() {
		<-sigs
		fmt.Fprintln(os.Stderr, "alborz: forced exit")
		os.Exit(1)
	}()
	e.Shutdown(ctx)
	cancel()

	s.Close()
}
