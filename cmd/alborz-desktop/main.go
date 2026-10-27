package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	_ "time/tzdata"

	"git.mehdix.org/alborz/cmd/alborz/app"
	"git.mehdix.org/alborz/internal/config"
	"git.mehdix.org/alborz/internal/desktop"
)

type uiActions struct {
	Open      func() error
	SetupCode func() string
	Close     func() error
}

func main() {
	runtime.LockOSThread()
	if err := run(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		showError(err.Error())
		os.Exit(1)
	}
}

func run() error {
	dir, err := desktop.DefaultDataDir()
	if err != nil {
		return err
	}
	options := app.Options{Addons: make(map[string]any)}
	fs := flag.NewFlagSet("alborz-desktop", flag.ContinueOnError)
	fs.String("config", "", "configuration file (or LBRZ_CONFIG); none disables file loading")
	addr := fs.String("addr", desktop.Address, "IPv4 loopback listening address")
	fs.StringVar(&options.DataDir, "data-dir", dir, "directory keeping application data")
	fs.StringVar(&options.LoginKey, "login-key", "", "base64-encoded encryption key; generated for a fresh installation")
	fs.BoolVar(&options.Cache, "cache", true, "cache server responses")
	fs.BoolVar(&options.Debug, "debug", false, "enable debug logs and local templates and assets")
	fs.BoolVar(&options.PrivateServices, "private-services", false, "allow private or plain HTTP account services")
	fs.StringVar(&options.ProjectURL, "project-url", "", "footer project link")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	var emptyKey bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "login-key" && f.Value.String() == "" {
			emptyKey = true
		}
	})
	cfg, err := config.Load(fs, os.LookupEnv)
	if err != nil {
		return err
	}
	if err := config.ApplyEnvironment(fs, os.LookupEnv); err != nil {
		return err
	}
	if emptyKey && options.LoginKey == "" {
		return errors.New("-login-key is empty")
	}
	if key, present := os.LookupEnv("LBRZ_LOGIN_KEY"); present && key == "" {
		return errors.New("LBRZ_LOGIN_KEY is empty")
	}
	if err := cfg.Servers(&options.Options.Options, fs.Args(), os.LookupEnv); err != nil {
		return err
	}
	options.SaveSetup = cfg.SaveSetup
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	instance, reused, err := desktop.Start(ctx, desktop.Options{DataDir: options.DataDir, Address: *addr, Open: openBrowser, New: func(dir, origin string) (desktop.Service, error) {
		if err := cfg.EnsureKey(&options.Options.Options); err != nil {
			return desktop.Service{}, err
		}
		logFile, err := os.OpenFile(filepath.Join(dir, "alborz.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return desktop.Service{}, err
		}
		options.CacheDir = filepath.Join(dir, "cache")
		options.PublicURL = origin
		server, handler, err := app.New(log.New(logFile, "", log.LstdFlags), options)
		if err != nil {
			logFile.Close()
			return desktop.Service{}, err
		}
		return desktop.Service{Handler: handler, SetupCode: server.SetupCode, StopStreams: server.Changes.Close, Close: func() { server.Close(); logFile.Close() }}, nil
	}})
	if err != nil {
		return fmt.Errorf("Alborz could not start: %w", err)
	}
	if reused {
		return nil
	}
	defer instance.Close()
	failed := make(chan error, 1)
	go func() {
		select {
		case err := <-instance.Errors:
			failed <- err
			cancel()
		case <-ctx.Done():
		}
	}()
	if err = runUI(ctx, uiActions{Open: func() error { return openBrowser(instance.URL) }, SetupCode: instance.SetupCode, Close: instance.Close}); err != nil {
		return err
	}
	if err = instance.Close(); err != nil {
		return err
	}
	select {
	case err := <-failed:
		return err
	default:
		return nil
	}
}
