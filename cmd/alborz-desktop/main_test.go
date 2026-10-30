package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"

	"git.mehdix.org/alborz/cmd/alborz/app"
	"git.mehdix.org/alborz/internal/desktop"
)

func TestDesktopStartsCommissioningWithoutExposingOwnerCode(t *testing.T) {
	instance, _, err := desktop.Start(context.Background(), desktop.Options{DataDir: t.TempDir(), Address: "127.0.0.1:0", Open: func(string) error { return nil }, New: func(dir, origin string) (desktop.Service, error) {
		options := app.Options{Addons: make(map[string]any),
			LoginKey:  "YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=",
			DataDir:   dir,
			PublicURL: origin}
		server, handler, err := app.New(log.New(io.Discard, "", 0), options)
		if err != nil {
			return desktop.Service{}, err
		}
		return desktop.Service{Handler: handler, SetupCode: server.SetupCode, Close: server.Close, StopStreams: server.Changes.Close}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	code := instance.SetupCode()
	if code == "" {
		t.Fatal("native UI has no owner code")
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Get(instance.URL + "/setup")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("setup returned%d: %s", response.StatusCode, body)
	}
	if strings.Contains(string(body), code) || strings.Contains(response.Request.URL.String(), code) {
		t.Fatal("owner code exposed by public setup page")
	}
}
