package dav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestRetryAfterHeaderParse(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		h    string
		want time.Time
	}{
		{"", time.Time{}},
		{"120", now.Add(120 * time.Second)},
		{"0", time.Time{}},
		{"-5", time.Time{}},
		{"soon", time.Time{}},
		{now.Add(time.Hour).UTC().Format(http.TimeFormat), now.Add(time.Hour)},
		{now.Add(-time.Hour).UTC().Format(http.TimeFormat), time.Time{}},
	} {
		got := retryAfter(c.h, now)
		if c.want.IsZero() != got.IsZero() || (!c.want.IsZero() && got.Sub(c.want).Abs() > 2*time.Second) {
			t.Errorf("retryAfter(%q) = %v, want %v", c.h, got, c.want)
		}
	}
}

func TestThrottledReadAnswersAnErrorNamingWhen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := sourceRouter{
		sources: []Source{{ID: SourceDomain, URL: u}},
		trusted: http.DefaultTransport,
		sign:    func(*http.Request, Source) error { return nil },
	}
	req, err := http.NewRequestWithContext(context.Background(), "PROPFIND", Base().JoinPath("/@"+u.Host+"/cal/").String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := r.RoundTrip(req)
	if err == nil {
		t.Fatal("a throttled read stayed a response")
	}
	var thr *ThrottledError
	if !errors.As(err, &thr) {
		t.Fatalf("the throttle came back as %T: %v", err, err)
	}
	if thr.StatusCode != http.StatusTooManyRequests || thr.Host != u.Host {
		t.Fatalf("the throttle lost its answer: %+v", thr)
	}
	if at := thr.At; at.Before(time.Now().Add(6*time.Second)) || at.After(time.Now().Add(8*time.Second)) {
		t.Fatalf("the throttle's delay is %v, want ~7s", at)
	}
	if resp != nil {
		t.Fatal("a body came with the error")
	}
}

func TestThrottledWriteStaysAResponse(t *testing.T) {
	// The mirror reads a write's answer to undo the copy it applied, so it must not become an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := sourceRouter{
		sources: []Source{{ID: SourceDomain, URL: u}},
		trusted: http.DefaultTransport,
		sign:    func(*http.Request, Source) error { return nil },
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, Base().JoinPath("/@"+u.Host+"/cal/x.ics").String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := r.RoundTrip(req)
	if err != nil {
		t.Fatalf("a throttled write came back as %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the write's answer is %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestNextRetryIsTheSoonestThrottle(t *testing.T) {
	now := time.Now()
	sooner, later := now.Add(time.Minute), now.Add(5*time.Minute)
	wrap := func(err error) error { return fmt.Errorf("example.test: %w", err) }
	for _, c := range []struct {
		troubles []error
		want     time.Time
	}{
		{nil, time.Time{}},
		{[]error{errors.New("down")}, time.Time{}},
		{[]error{wrap(&ThrottledError{At: later}), errors.New("down")}, later},
		{[]error{wrap(&ThrottledError{At: later}), wrap(&ThrottledError{At: sooner})}, sooner},
		{[]error{wrap(&ThrottledError{}), wrap(&ThrottledError{At: later})}, later},
	} {
		if got := nextRetry(c.troubles); got != c.want {
			t.Errorf("nextRetry(%v) = %v, want %v", c.troubles, got, c.want)
		}
	}
}
