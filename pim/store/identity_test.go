package store

import (
	"bytes"
	"context"
	"git.mehdix.org/alborz/internal/kv"
	"github.com/emersion/go-webdav"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPUTPreservesResourceUID(t *testing.T) {
	for _, kind := range []Kind{Calendar, AddressBook} {
		t.Run(string(kind), func(t *testing.T) {
			s := open(t)
			const owner = "owner@example.org"
			coll := HomePath(kind, owner) + "identity/"
			method, typ, raw, suffix := "MKCALENDAR", "text/calendar", event, ".ics"
			if kind == AddressBook {
				method, typ, raw, suffix = "MKCOL", "text/vcard", "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:e1\r\nFN:Lunch\r\nEND:VCARD\r\n", ".vcf"
			}
			h := s.Handler()
			if got := serve(t, h, owner, method, coll, ""); got != 201 {
				t.Fatalf("create: %d", got)
			}
			put := func(name, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPut, coll+name+suffix, strings.NewReader(body))
				r.Header.Set("Content-Type", typ)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r.WithContext(As(r.Context(), owner)))
				return w
			}
			if got := put("one", raw).Code; got != 201 {
				t.Fatalf("put: %d", got)
			}
			refused := put("one", strings.ReplaceAll(raw, "e1", "e2"))
			if refused.Code < 400 || !strings.Contains(refused.Body.String(), "no-uid-conflict") {
				t.Errorf("UID replacement: %d %s", refused.Code, refused.Body)
			}
			ref := Ref{Owner: owner, ID: "identity"}
			old, err := s.Object(ref, "one"+suffix)
			if err != nil {
				t.Fatal(err)
			}
			if old.UID != "e1" || !bytes.Equal(old.Data, []byte(raw)) {
				t.Errorf("refused PUT changed bytes or UID: %+v", old)
			}
			if got := put("other", raw).Code; got < 400 {
				t.Errorf("original UID index lost: %d", got)
			}
			if got := put("two", strings.ReplaceAll(raw, "e1", "e2")).Code; got != 201 {
				t.Errorf("refused UID polluted index: %d", got)
			}
			if got := put("one", strings.ReplaceAll(raw, "Lunch", "Dinner")).Code; got/100 != 2 {
				t.Errorf("same UID edit refused: %d", got)
			}
		})
	}
}

func TestAtomicDeleteChecksBothConditions(t *testing.T) {
	for _, kind := range []Kind{Calendar, AddressBook} {
		t.Run(string(kind), func(t *testing.T) {
			s := open(t)
			const owner = "owner@example.org"
			coll := HomePath(kind, owner) + "delete/"
			method := "MKCALENDAR"
			if kind == AddressBook {
				method = "MKCOL"
			}
			if code := serve(t, s.Handler(), owner, method, coll, ""); code != 201 {
				t.Fatal(code)
			}
			ref := Ref{Owner: owner, ID: "delete"}
			old, err := s.PutObject(ref, "one", "e1", []byte("preserve bytes"), nil, owner)
			if err != nil {
				t.Fatal(err)
			}
			var remove func(context.Context, string, *webdav.RemoveAllOptions) error
			if kind == Calendar {
				b, ok := any(calendars{s}).(interface {
					DeleteCalendarObjectWithOptions(context.Context, string, *webdav.RemoveAllOptions) error
				})
				if !ok {
					t.Fatal("calendar backend has no atomic conditional deletion")
				}
				remove = b.DeleteCalendarObjectWithOptions
			} else {
				b, ok := any(books{s}).(interface {
					DeleteAddressObjectWithOptions(context.Context, string, *webdav.RemoveAllOptions) error
				})
				if !ok {
					t.Fatal("address backend has no atomic conditional deletion")
				}
				remove = b.DeleteAddressObjectWithOptions
			}
			ctx := As(context.Background(), owner)
			for _, opts := range []*webdav.RemoveAllOptions{
				{IfMatch: `"stale"`, IfNoneMatch: `"other"`},
				{IfMatch: `"stale"`, IfNoneMatch: `malformed`},
				{IfMatch: webdav.ConditionalMatch(`"` + old.ETag + `"`), IfNoneMatch: webdav.ConditionalMatch(`W/"` + old.ETag + `"`)},
				{IfMatch: `W/"` + webdav.ConditionalMatch(old.ETag) + `"`},
			} {
				if err := remove(ctx, coll+"one", opts); err == nil {
					t.Error("delete accepted failed condition")
				} else if code, _ := webdav.HTTPErrorCode(err); code != http.StatusPreconditionFailed {
					t.Errorf("condition returned %v, want 412", err)
				}
				held, err := s.Object(ref, "one")
				if err != nil || held == nil || !bytes.Equal(held.Data, old.Data) {
					t.Fatalf("delete changed object: %+v %v", held, err)
				}
				s.db.View(func(tx kv.Tx) error {
					if string(tx.Get(s.uidKey(ref, "e1"))) != "one" {
						t.Error("delete changed UID index")
					}
					return nil
				})
			}
			if err := remove(ctx, coll+"one", &webdav.RemoveAllOptions{IfMatch: webdav.ConditionalMatch(`"` + old.ETag + `"`), IfNoneMatch: `"other"`}); err != nil {
				t.Fatalf("valid deletion: %v", err)
			}
			if _, err := s.Object(ref, "one"); err == nil {
				t.Error("valid delete kept object")
			}
		})
	}
}
