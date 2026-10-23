package dav

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func TestSyncPagesKeepTheLastChange(t *testing.T) {
	update := sent{path: "/cal/a.ics", etag: `"new"`, data: []byte("latest")}
	missing := sent{path: update.path, etag: `"old"`}
	for _, tc := range []struct {
		name  string
		pages []synced
		want  string
	}{
		{"update then delete", []synced{{put: []sent{update}}, {gone: []string{update.path}}}, ""},
		{"delete then update", []synced{{gone: []string{update.path}}, {put: []sent{update}}}, "latest"},
		{"missing then delete", []synced{{put: []sent{missing}}, {gone: []string{update.path}}}, ""},
		{"missing then update", []synced{{put: []sent{missing}}, {put: []sent{update}}}, "latest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := 0
			w := wire{sync: func(_ context.Context, _ *http.Client, _ *url.URL, _ string) (synced, error) {
				s := tc.pages[page]
				page++
				s.token = "final"
				s.truncated = page < len(tc.pages)
				return s, nil
			}, get: func(_ context.Context, _ *http.Client, _ *url.URL, paths []string) ([]sent, error) {
				t.Errorf("obsolete multiget: %v", paths)
				return []sent{{path: update.path, data: []byte("obsolete")}}, nil
			}}
			target, _ := url.Parse("https://example.org/cal/")
			d, err := w.syncCollection(context.Background(), http.DefaultClient, target, "previous")
			if err != nil {
				t.Fatal(err)
			}
			held := map[string]string{"a.ics": "before"}
			for _, n := range d.gone {
				delete(held, n)
			}
			for _, o := range d.put {
				held[o.Name] = string(o.Data)
			}
			if got := held["a.ics"]; got != tc.want {
				t.Errorf("final object %q, want %q", got, tc.want)
			}
			if d.token != "final" {
				t.Errorf("token %q", d.token)
			}
		})
	}
}
