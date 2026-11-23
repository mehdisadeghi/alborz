package store

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A limit names the collection it was made for: one made again under the name, or imported over it, is
// another, and a limit of none reaches none (ADR 109).
func TestALimitDoesNotFollowACollectionMadeAgain(t *testing.T) {
	store := open(t)
	h := store.Handler()
	const owner = "owner@example.org"
	ref := Ref{Owner: owner, ID: "work"}
	path := HomePath(Calendar, owner) + "work/"
	limited := func(names []string, method, path string) int {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(""))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r.WithContext(Limited(As(r.Context(), owner), names)))
		return w.Code
	}
	name := func() string {
		t.Helper()
		c, err := store.Collection(ref)
		if err != nil {
			t.Fatal(err)
		}
		return store.LimitName(*c)
	}
	write := func() {
		t.Helper()
		if got := serve(t, h, owner, http.MethodPut, path+"e1.ics", event); got != http.StatusCreated {
			t.Fatalf("writing the event: got %d", got)
		}
	}
	if got := serve(t, h, owner, "MKCALENDAR", path, ""); got != http.StatusCreated {
		t.Fatalf("making the calendar: got %d", got)
	}
	write()
	first := name()
	if got := limited([]string{first}, http.MethodGet, path+"e1.ics"); got != http.StatusOK {
		t.Fatalf("a read within the limit: got %d", got)
	}
	if got := limited(nil, http.MethodGet, path+"e1.ics"); got != http.StatusNotFound {
		t.Fatalf("a read with a limit of none: got %d", got)
	}

	archive := map[string][]byte{}
	if err := store.Export(owner, func(name string, data []byte) error { archive[name] = data; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Import(owner, archive); err != nil {
		t.Fatal(err)
	}
	imported := name()
	if got := limited([]string{first}, http.MethodGet, path+"e1.ics"); got != http.StatusNotFound || imported == first {
		t.Fatalf("a read of the imported calendar with the limit made before: got %d", got)
	}

	if err := store.Delete(ref); err != nil {
		t.Fatal(err)
	}
	if got := serve(t, h, owner, "MKCALENDAR", path, ""); got != http.StatusCreated {
		t.Fatalf("making the calendar again: got %d", got)
	}
	write()
	for _, old := range []string{first, imported} {
		if got := limited([]string{old}, http.MethodGet, path+"e1.ics"); got != http.StatusNotFound || name() == old {
			t.Fatalf("a read of the calendar made again with a limit made before: got %d", got)
		}
	}
}
