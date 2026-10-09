package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	// Calendars name zones the host may not hold, such as US/Central on
	// a system without tzdata-legacy; the host's database still comes first.
	_ "time/tzdata"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/cmd/alborz/app"
)

// serveProfiles listens only locally: a heap profile holds passwords and mail.
func serveProfiles(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip, err := netip.ParseAddr(host); host != "localhost" && (err != nil || !ip.IsLoopback()) {
		return fmt.Errorf("the profiles hold the process's memory; give a loopback address such as localhost:6060")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { log.Printf("profiles: %v", server.Serve(ln)) }()
	return nil
}

func main() {
	err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout)
	var usage usageError
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
	case errors.As(err, &usage):
		// The flags said what was wrong, with the usage.
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "alborz: %v\n", err)
		os.Exit(1)
	}
}

// usageError is a command line the flags refused, already said with the usage.
type usageError struct{ error }

var addonFlags []func(*flag.FlagSet, *app.Options) func()

// run serves alborz until ctx ends or a signal stops it; the environment comes through getenv.
func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := flag.NewFlagSet("alborz", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var (
		addr        string
		profileAddr string
		proxies     string
		options     app.Options
	)
	options.Addons = make(map[string]any)
	fs.StringVar(&addr, "addr", ":1323", "listening address")
	fs.StringVar(&profileAddr, "pprof", "",
		"loopback address serving Go's profiles, the goroutine leak profile among them; unset serves none")
	fs.BoolVar(&options.Debug, "debug", false, "enable debug logs, and read templates and assets from ./templates")
	fs.BoolVar(&options.PrivateServices, "private-services", false,
		"let an account name a calendar or contacts server on plain HTTP or a private address")
	fs.StringVar(&proxies, "trusted-proxy", "",
		"comma-separated addresses or CIDR ranges of the proxies in front of alborz, whose X-Forwarded-For names the reader and X-Forwarded-Proto the scheme; unset takes the connection's own")
	fs.StringVar(&options.PublicURL, "public-url", getenv("LBRZ_PUBLIC_URL"),
		"where readers reach alborz, such as https://mail.example.org, for links in mail and addresses shown to other clients (or $LBRZ_PUBLIC_URL); unset uses each request's own")
	// The environment keeps the key out of the process list.
	fs.StringVar(&options.LoginKey, "login-key", getenv("LBRZ_LOGIN_KEY"), "key sealing what alborz keeps, 32 bytes in base64 (or $LBRZ_LOGIN_KEY); required")
	fs.BoolVar(&options.Cache, "cache", true,
		"keep what the servers answered, so pages open without asking again; false asks every time and keeps nothing")
	fs.StringVar(&options.DataDir, "data-dir", ".",
		"directory keeping alborz.db: remembered logins and settings, sealed under the login key, and the calendars and address books kept here")
	fs.StringVar(&options.ProjectURL, "project-url", "",
		"where the footer's project name links; unset prints the name alone")
	var addonValues []func()
	for _, register := range addonFlags {
		addonValues = append(addonValues, register(fs, &options))
	}
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: alborz [options...] <upstreams...>

Plain imap[s]://, smtp[s]://, sieve://, https://, poppassd:// or +insecure URLs
configure a single provider accepting logins of any domain. Alternatively,
each argument serves one mail domain and logins are only accepted for the
listed domains: a bare domain uses SRV auto-discovery, and explicit
upstreams are given as repeated domain=url arguments, e.g.:

  alborz example.org example.com=imaps://mail.example.com

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return err
	} else if err != nil {
		return usageError{err}
	}
	for _, apply := range addonValues {
		apply()
	}
	options.Upstreams = fs.Args()
	if len(options.Upstreams) == 0 {
		fmt.Fprintln(fs.Output(), "alborz: no upstream servers specified")
		fs.Usage()
		return usageError{errors.New("no upstream servers specified")}
	}
	options.TrustedProxies = strings.FieldsFunc(proxies, func(r rune) bool { return r == ',' || r == ' ' })
	if options.Cache {
		dir, err := os.UserCacheDir()
		if err != nil {
			return fmt.Errorf("no cache directory: %v; set $XDG_CACHE_HOME or run with -cache=false", err)
		}
		options.CacheDir = filepath.Join(dir, alborz.AppName)
	}
	if profileAddr != "" {
		if err := serveProfiles(profileAddr); err != nil {
			return fmt.Errorf("-pprof %s: %v", profileAddr, err)
		}
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)
	s, handler, err := app.New(logger, options)
	if err != nil {
		return err
	}
	defer s.Close()

	// Bind before announcing, so the prologue is true.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, alborz.Prologue)

	server := &http.Server{Handler: handler}
	server.RegisterOnShutdown(s.Changes.Close)
	served := make(chan error, 1)
	go func() { served <- server.Serve(ln) }()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	for stop := false; !stop; {
		select {
		case err := <-served:
			return err
		case <-ctx.Done():
			stop = true
		case sig := <-sigs:
			if sig != syscall.SIGUSR1 {
				stop = true
				break
			}
			if err := s.Reload(); err != nil {
				logger.Printf("Failed to reload server: %v", err)
			}
		}
	}

	fmt.Fprintln(os.Stderr, "alborz: shutting down (up to 5s for active requests; interrupt again to force)")
	go func() {
		<-sigs
		fmt.Fprintln(os.Stderr, "alborz: forced exit")
		os.Exit(1)
	}()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}
