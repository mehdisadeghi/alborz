package dav

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz/internal/kv"
)

type moveTransport func(*http.Request) (*http.Response, error)

func (f moveTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMoveDestinationIsRoutedToTheSameServer(t *testing.T) {
	u, _ := url.Parse("https://dav.example/cal/")
	sources := []Source{{ID: SourceDomain, URL: u}}
	req, _ := http.NewRequest("MOVE", Base().String()+"@dav.example/cal/old/a.ics", nil)
	req.Header.Set("Destination", Base().String()+"@dav.example/cal/new/a.ics")
	_, out, err := unmarked(sources, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Header.Get("Destination"); got != "https://dav.example/cal/new/a.ics" {
		t.Fatalf("upstream destination = %q", got)
	}
	if got := req.Header.Get("Destination"); !strings.Contains(got, "/@dav.example/") {
		t.Fatal("routing mutated the caller's request")
	}
	req.Header.Set("Destination", "https://untrusted.example/cal/a.ics")
	if _, _, err := unmarked(sources, req); err == nil {
		t.Fatal("MOVE accepted a destination outside the routed DAV source")
	}
}

func TestNativeMoveInvalidatesBothMirroredCollections(t *testing.T) {
	m, _ := through(t, newFake())
	u := m.user(someone)
	for _, coll := range []string{"/old/", "/new/"} {
		if err := m.change(u, coll, func(tx kv.Tx) error {
			if err := m.put(tx, m.heldKey(u, coll), mirrored{URL: "https://dav.test" + coll, Synced: time.Now()}); err != nil {
				return err
			}
			return m.put(tx, m.objectsKey(u, coll)+m.names.Of("a.ics"), object{Name: "a.ics", ETag: `"original"`, Data: []byte(event("a"))})
		}); err != nil {
			t.Fatal(err)
		}
		if objects, err := m.objects(u, coll); err != nil || len(objects) != 1 {
			t.Fatalf("priming %s: %v, %v", coll, objects, err)
		}
	}
	client := &http.Client{Transport: m.Transport(someone, moveTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	}))}
	if handled, err := MoveObject(context.Background(), client, "/old/a.ics", "/new/a.ics", `"original"`); !handled || err != nil {
		t.Fatalf("moving: handled=%t, error=%v", handled, err)
	}
	for _, coll := range []string{"/old/", "/new/"} {
		if _, held, err := m.held(u, coll); err != nil || held {
			t.Fatalf("%s still has a stale held collection: %t, %v", coll, held, err)
		}
		if objects, err := m.objects(u, coll); err != nil || len(objects) != 0 {
			t.Fatalf("%s still has stale objects: %v, %v", coll, objects, err)
		}
		m.mu.Lock()
		writes := m.reader(u).coll(coll).writes
		m.mu.Unlock()
		if writes != 1 {
			t.Fatalf("%s allows a sync from before the move to restore stale objects", coll)
		}
	}
}

func TestNativeMoveRequiresStrongSourceVersion(t *testing.T) {
	client := &http.Client{Transport: moveTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("a move without a strong version reached the server")
		return nil, nil
	})}
	for _, etag := range []string{"", `W/"weak"`} {
		if handled, err := MoveObject(context.Background(), client, "/old/a.ics", "/new/a.ics", etag); !handled || err == nil {
			t.Fatalf("etag %q: handled=%t, error=%v", etag, handled, err)
		}
	}
}

func TestNativeMoveIsConditionalAndFallsBackOnlyWhenUnsupported(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusNoContent, http.StatusMethodNotAllowed, http.StatusNotImplemented, http.StatusForbidden, http.StatusPreconditionFailed, http.StatusMultiStatus} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := &http.Client{Transport: moveTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "MOVE" || r.Header.Get("Overwrite") != "F" || r.Header.Get("If-Match") != `"original"` || !strings.HasSuffix(r.Header.Get("Destination"), "/new/a.ics") {
					t.Fatalf("unsafe MOVE request: %s %v", r.Method, r.Header)
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			handled, err := MoveObject(context.Background(), client, "/old/a.ics", "/new/a.ics", `"original"`)
			unsupported := status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented
			if handled == unsupported || (err != nil) != (!unsupported && status != http.StatusCreated && status != http.StatusNoContent) {
				t.Fatalf("status %d: handled=%t, error=%v", status, handled, err)
			}
		})
	}
}
