package dav_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-webdav"
)

func TestRemoveObjectRequiresTheVersionRead(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodDelete || r.Header.Get("If-Match") != `"original"` || r.Header.Get("Schedule-Reply") != "F" {
			t.Errorf("unsafe deletion: %s, %v", r.Method, r.Header)
		}
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer s.Close()
	for _, etag := range []string{"", `W/"weak"`, `"original"`} {
		err := dav.RemoveObject(context.Background(), s.Client(), s.URL+"/event.ics", etag)
		if code, _ := webdav.HTTPErrorCode(err); code != http.StatusPreconditionFailed {
			t.Errorf("etag %q: got %v, want 412", etag, err)
		}
	}
	if requests != 1 {
		t.Errorf("sent %d deletions, want only the strong ETag", requests)
	}
}
