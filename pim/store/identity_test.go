package store

import (
	"bytes"
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
