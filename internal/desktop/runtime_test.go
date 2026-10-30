package desktop

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestInstanceReuseAndIsolation(t *testing.T) {
	dir := t.TempDir()
	var opened, created atomic.Int32
	options := Options{DataDir: dir, Address: "127.0.0.1:0", Open: func(string) error { opened.Add(1); return nil }, New: func(_, _ string) (Service, error) {
		created.Add(1)
		return Service{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("app")) })}, nil
	}}
	instance, reused, err := Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if reused {
		t.Fatal("first instance reused")
	}
	_, reused, err = Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reused || opened.Load() != 1 || created.Load() != 1 {
		t.Fatal("second launch did not reuse first")
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	for _, test := range []struct {
		path, host, origin string
		status             int
	}{
		{"/", "evil.test", "", 403}, {"/", "", "https://evil.test", 403}, {"/", "", "", 200},
	} {
		req, _ := http.NewRequest("GET", instance.URL+test.path, nil)
		if test.host != "" {
			req.Host = test.host
		}
		if test.origin != "" {
			req.Header.Set("Origin", test.origin)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatalf("%+v: status %d", test, response.StatusCode)
		}
	}
	if err = instance.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "instance.url")); !os.IsNotExist(err) {
		t.Fatal("descriptor survived shutdown")
	}
	response, err := client.Get(instance.URL)
	if err == nil {
		response.Body.Close()
		t.Fatal("server still accepts requests after shutdown")
	}
	next, reused, err := Start(context.Background(), options)
	if err != nil || reused {
		t.Fatalf("shutdown retained instance ownership: reused=%v, err=%v", reused, err)
	}
	next.Close()

}

func TestStartupFailureReleasesOwnership(t *testing.T) {
	dir := t.TempDir()
	options := Options{DataDir: dir, Address: "127.0.0.1:0", Open: func(string) error { return nil }, New: func(string, string) (Service, error) { return Service{}, errors.New("database unavailable") }}
	if _, _, err := Start(context.Background(), options); err == nil {
		t.Fatal("startup error ignored")
	}
	options.New = func(string, string) (Service, error) { return Service{Handler: http.NotFoundHandler()}, nil }
	instance, reused, err := Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if reused {
		t.Fatal("failed startup retained ownership")
	}
}

func TestRejectNonLoopbackListener(t *testing.T) {
	for _, address := range []string{":1323", "0.0.0.0:1323", "192.0.2.1:1323", "localhost:1323"} {
		if _, _, err := Start(context.Background(), Options{Address: address}); err == nil {
			t.Fatalf("accepted %q", address)
		}
	}
}

func TestPortCollisionDoesNotCreateStateOrOpenAnotherService(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, _, err = Start(context.Background(), Options{DataDir: t.TempDir(), Address: listener.Addr().String(), Open: func(string) error { t.Fatal("opened unrelated service"); return nil }, New: func(string, string) (Service, error) {
		t.Fatal("created application on occupied port")
		return Service{}, nil
	}})
	if err == nil {
		t.Fatal("port collision created state or passed silently")
	}
}
