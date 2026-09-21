package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.mehdix.org/alborz"
	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/labstack/gommon/log"

	_ "git.mehdix.org/alborz/plugins/base"
	_ "git.mehdix.org/alborz/plugins/caldav"
	_ "git.mehdix.org/alborz/plugins/carddav"
	_ "git.mehdix.org/alborz/plugins/sieve"
	_ "git.mehdix.org/alborz/plugins/viewhtml"
	_ "git.mehdix.org/alborz/plugins/viewtext"
)

var themesPath = "./themes"

// serveProfiles answers Go's profiles on their own listener. A heap
// profile is the process's memory - passwords, mail - so the address
// must be one only this machine reaches.
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

// devVersion is what a build off main calls itself: it is not a
// release, and saying so is more useful than a number nobody cut.
const devVersion = "dev"

// version is stamped at link time with the tag being built, or with
// devVersion off main. It is empty for a local build, which has only
// the VCS metadata below to go on.
var version string

// modified, when the build stamps it "true" or "false", replaces the go
// tool's own word on whether the tree differed from the revision. The
// go tool counts every file git does not know, so a note or a draft
// lying in the checkout made every build dirty, which says nothing of
// the binary; the Makefile asks only about what the build reads. A
// build that does not pass it gets the go tool's answer, as before.
var modified string

// buildVersion is what the footer and the User-Agent report. A release
// names itself and nothing else; anything else carries the revision,
// since that is what identifies the build being run.
func buildVersion() string {
	revision, date := vcsStamp()
	stamp := strings.TrimSpace(revision + " " + date)
	switch {
	case version == "":
		return stamp
	case version == devVersion:
		return strings.TrimSpace(version + " " + stamp)
	default:
		return version
	}
}

// buildName is what tells one build from another, for the rail's head
// where a reader reporting a fault can see it: the revision, which
// every build has, and the tag after it when the commit is one.
func buildName() string {
	revision, _ := vcsStamp()
	if version == "" || version == devVersion {
		return revision
	}
	if revision == "" {
		return version
	}
	return revision + " @ " + version
}

// vcsStamp is the revision and date the go tool embeds into the binary;
// empty for unstamped builds such as go run.
func vcsStamp() (string, string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", ""
	}
	var revision, date string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			date, _, _ = strings.Cut(s.Value, "T")
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if revision == "" {
		return "", ""
	}
	if modified != "" {
		var err error
		if dirty, err = strconv.ParseBool(modified); err != nil {
			panic(fmt.Sprintf("main.modified stamped as %q: %v", modified, err))
		}
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if dirty {
		revision += "-dirty"
	}
	return revision, date
}

// defaultCacheDir is the XDG cache directory: $XDG_CACHE_HOME, or
// ~/.cache. A deployment that keeps it elsewhere says so with
// -cache-dir.
func defaultCacheDir() string {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, alborz.AppName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// The spec's own default for an unset XDG_CACHE_HOME.
	return filepath.Join(home, ".cache", alborz.AppName)
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
	flag.StringVar(&loginKey, "login-key", "", "Fernet key for login persistence (or $LBRZ_LOGIN_KEY)")
	flag.StringVar(&options.CacheDir, "cache-dir", defaultCacheDir(),
		"directory keeping the calendar and contacts cache between runs, sealed under the login key; empty keeps it in memory")
	// The working directory, the way a daemon told nothing keeps its
	// state: the unit says where alborz runs and the file lands there.
	flag.StringVar(&options.DataDir, "data-dir", ".",
		"directory keeping alborz.db: remembered logins and reading settings, sealed under the login key, and the calendars and address books kept here; empty keeps none of them")
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
		if !strings.Contains(p, "/") {
			if ip := net.ParseIP(p); ip != nil && ip.To4() != nil {
				p += "/32"
			} else {
				p += "/128"
			}
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			fmt.Fprintf(flag.CommandLine.Output(), "alborz: invalid -trusted-proxy %q: %v\n", p, err)
			os.Exit(2)
		}
		options.TrustedProxies = append(options.TrustedProxies, n)
	}

	if publicURL == "" {
		publicURL = os.Getenv("LBRZ_PUBLIC_URL")
	}
	if publicURL != "" {
		u, err := url.Parse(publicURL)
		// alborz answers at the root of its host; a path would be a
		// prefix it does not serve under.
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
	options.Version = buildVersion()
	options.Build = buildName()
	options.Revision, _ = vcsStamp()

	if loginKey != "" {
		fernetKey, err := fernet.DecodeKey(loginKey)
		if err != nil {
			fmt.Fprintf(flag.CommandLine.Output(), "alborz: invalid -login-key: %v\n", err)
			os.Exit(2)
		}
		options.LoginKey = fernetKey
	}

	e := echo.New()
	e.HideBanner = true
	if l, ok := e.Logger.(*log.Logger); ok {
		// Logs are diagnostics; the useful startup line is the prologue
		// on stdout, so the log stream never drowns it.
		l.SetOutput(os.Stderr)
		l.SetHeader("${time_rfc3339} ${level}")
	}
	s, err := alborz.New(e, &options)
	if err != nil {
		e.Logger.Fatal(err)
	}
	e.Use(middleware.Recover())
	if options.Debug {
		// The completion logger above stays silent for a request that
		// never finishes; log arrivals too, so a hang names its URI.
		e.Pre(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				e.Logger.Printf("-> %s %s", c.Request().Method, c.Request().RequestURI)
				return next(c)
			}
		})
		e.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
			Format: "${time_rfc3339} method=${method}, uri=${uri}, status=${status}\n",
		}))
		e.Logger.SetLevel(log.DEBUG)
	}

	// Bind before announcing, so the prologue only says the server is up
	// once the port is actually open.
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
