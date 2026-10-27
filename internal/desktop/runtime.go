// Package desktop owns the local application's lifetime, independently of its native UI.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const Address = "127.0.0.1:1323"

type Service struct {
	Handler     http.Handler
	SetupCode   func() string
	Close       func()
	StopStreams func()
}

type Options struct {
	DataDir string
	Address string
	Open    func(string) error
	New     func(dataDir, origin string) (Service, error)
}

type Instance struct {
	URL        string
	Errors     <-chan error
	SetupCode  func() string
	server     *http.Server
	service    Service
	lock       *os.File
	descriptor string
	once       sync.Once
	closeErr   error
}

// Start owns a new server or opens the existing instance in this process’s browser.
func Start(ctx context.Context, options Options) (*Instance, bool, error) {
	if options.Address == "" {
		options.Address = Address
	}
	host, _, err := net.SplitHostPort(options.Address)
	if err != nil || host != "127.0.0.1" {
		return nil, false, errors.New("desktop listener must use IPv4 loopback")
	}
	if options.New == nil || options.Open == nil {
		return nil, false, errors.New("incomplete desktop options")
	}
	if err = secureDirectory(options.DataDir); err != nil {
		return nil, false, err
	}
	lock, owned, err := acquireLock(filepath.Join(options.DataDir, "instance.lock"))
	if err != nil {
		return nil, false, err
	}
	path := filepath.Join(options.DataDir, "instance.url")
	if !owned {
		return nil, true, openExisting(ctx, path, options.Open)
	}
	success := false
	defer func() {
		if !success {
			releaseLock(lock)
		}
	}()
	ln, err := net.Listen("tcp4", options.Address)
	if err != nil {
		return nil, false, fmt.Errorf("cannot start Alborz on %s: %w", options.Address, err)
	}
	defer func() {
		if !success {
			ln.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	origin := fmt.Sprintf("http://localhost:%d", port)
	service, err := options.New(options.DataDir, origin)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if !success && service.Close != nil {
			service.Close()
		}
	}()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != fmt.Sprintf("localhost:%d", port) || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		service.Handler.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	if service.StopStreams != nil {
		server.RegisterOnShutdown(service.StopStreams)
	}
	served := make(chan error, 1)
	instance := &Instance{URL: origin, Errors: served, SetupCode: service.SetupCode, server: server, service: service, lock: lock, descriptor: path}
	if instance.SetupCode == nil {
		instance.SetupCode = func() string { return "" }
	}
	if err = writeDescriptor(path, []byte(origin)); err != nil {
		return nil, false, err
	}
	success = true
	go func() {
		err := server.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		served <- err
	}()
	return instance, false, nil
}

func writeDescriptor(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".instance-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err = temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	// Only the process holding the lifetime lock can publish this descriptor.
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

func openExisting(ctx context.Context, path string, open func(string) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			address := string(data)
			u, err := url.Parse(address)
			if err != nil || u.Scheme != "http" || u.Hostname() != "localhost" || u.Port() == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return errors.New("invalid desktop instance address")
			}
			return open(address)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.New("Alborz is already starting; try again shortly")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (i *Instance) Close() error {
	i.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		i.closeErr = i.server.Shutdown(ctx)
		if i.closeErr != nil {
			i.server.Close()
		}
		if i.service.Close != nil {
			i.service.Close()
		}
		if err := os.Remove(i.descriptor); err != nil && !errors.Is(err, os.ErrNotExist) && i.closeErr == nil {
			i.closeErr = err
		}
		releaseLock(i.lock)
	})
	return i.closeErr
}
