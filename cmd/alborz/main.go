package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/labstack/gommon/log"
	"git.mehdix.org/alborz"

	_ "git.mehdix.org/alborz/plugins/base"
	_ "git.mehdix.org/alborz/plugins/caldav"
	_ "git.mehdix.org/alborz/plugins/carddav"
	_ "git.mehdix.org/alborz/plugins/lua"
	_ "git.mehdix.org/alborz/plugins/sieve"
	_ "git.mehdix.org/alborz/plugins/viewhtml"
	_ "git.mehdix.org/alborz/plugins/viewtext"
)

var themesPath = "./themes"

// Version from the VCS metadata the go tool embeds into the binary;
// empty for unstamped builds such as go run.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
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
		return ""
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if dirty {
		revision += "-dirty"
	}
	return strings.TrimSpace(revision + " " + date)
}

func main() {
	var (
		addr     string
		loginKey string
		options  alborz.Options
	)
	flag.StringVar(&options.Theme, "theme", "", "default theme")
	flag.StringVar(&addr, "addr", ":1323", "listening address")
	flag.BoolVar(&options.Debug, "debug", false, "enable debug logs")
	flag.StringVar(&loginKey, "login-key", "", "Fernet key for login persistence (or $LBRZ_LOGIN_KEY)")

	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `usage: alborz [options...] <upstreams...>

Plain imap[s]://, smtp[s]://, sieve://, https:// or +insecure URLs
configure a single provider accepting logins of any domain. Alternatively,
each argument serves one mail domain and logins are only accepted for the
listed domains: a bare domain uses SRV auto-discovery, and explicit
upstreams are given as repeated domain=url arguments, e.g.:

  alps example.org example.com=imaps://mail.example.com

`)
		flag.PrintDefaults()
	}

	flag.Parse()

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
		l.SetHeader("${time_rfc3339} ${level}")
	}
	s, err := alborz.New(e, &options)
	if err != nil {
		e.Logger.Fatal(err)
	}
	e.Use(middleware.Recover())
	if options.Debug {
		e.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
			Format: "${time_rfc3339} method=${method}, uri=${uri}, status=${status}\n",
		}))
		e.Logger.SetLevel(log.DEBUG)
	}

	go e.Start(addr)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGINT)

	for sig := range sigs {
		if sig == syscall.SIGUSR1 {
			if err := s.Reload(); err != nil {
				e.Logger.Errorf("Failed to reload server: %v", err)
			}
		} else if sig == syscall.SIGINT {
			break
		}
	}

	ctx, cancel := context.WithDeadline(context.Background(),
		time.Now().Add(30*time.Second))
	e.Shutdown(ctx)
	cancel()

	s.Close()
}
