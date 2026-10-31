package store

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.mehdix.org/alborz/internal/kv"
)

// Run a competing write immediately before the delete's transaction starts.
type beforeUpdate struct {
	kv.DB
	next func()
}

func (db *beforeUpdate) Update(fn func(kv.Tx) error) error {
	if next := db.next; next != nil {
		db.next = nil
		next()
	}
	return db.DB.Update(fn)
}

func TestDeleteChecksBothConditionsAtomically(t *testing.T) {
	const owner = "owner@example.org"
	for _, kind := range []Kind{Calendar, AddressBook} {
		t.Run(string(kind), func(t *testing.T) {
			for _, test := range []struct {
				name, match, none string
				change            bool
				want              int
			}{
				{"stale match", `"stale"`, `"other"`, false, http.StatusPreconditionFailed},
				{"weak match", `W/"held"`, `"other"`, false, http.StatusPreconditionFailed},
				{"weak exclusion", `"held"`, `W/"held"`, false, http.StatusPreconditionFailed},
				{"malformed exclusion", `"stale"`, `malformed`, false, http.StatusPreconditionFailed},
				{"both pass", `"held"`, `"other"`, false, http.StatusNoContent},
				{"match changed", `"held"`, `"other"`, true, http.StatusPreconditionFailed},
				{"exclusion changed", `*`, `W/"changed"`, true, http.StatusPreconditionFailed},
			} {
				t.Run(test.name, func(t *testing.T) {
					s := open(t)
					ref := Ref{Owner: owner, ID: "work"}
					if _, err := s.Create(Collection{Owner: owner, ID: ref.ID, Kind: kind}); err != nil {
						t.Fatal(err)
					}
					held, err := s.PutObject(ref, "item", "item", []byte("held"), nil, owner)
					if err != nil {
						t.Fatal(err)
					}
					match := strings.NewReplacer("held", held.ETag, "changed", ETagOf([]byte("changed")))
					if test.change {
						writer := *s
						s.db.db = &beforeUpdate{DB: s.db.db, next: func() {
							if _, err := writer.PutObject(ref, "item", "item", []byte("changed"), nil, owner); err != nil {
								t.Fatal(err)
							}
						}}
					}
					r := httptest.NewRequest(http.MethodDelete, HomePath(kind, owner)+"work/item", nil)
					r.Header.Set("If-Match", match.Replace(test.match))
					r.Header.Set("If-None-Match", match.Replace(test.none))
					w := httptest.NewRecorder()
					s.Handler().ServeHTTP(w, r.WithContext(As(r.Context(), owner)))
					if w.Code != test.want {
						t.Fatalf("DELETE: got %d, want %d: %s", w.Code, test.want, w.Body.String())
					}
					if test.want == http.StatusPreconditionFailed {
						o, err := s.Object(ref, "item")
						if err != nil {
							t.Fatalf("rejected delete lost the object: %v", err)
						}
						want := "held"
						if test.change {
							want = "changed"
						}
						if string(o.Data) != want {
							t.Fatalf("object = %q, want %q", o.Data, want)
						}
						s.db.View(func(tx kv.Tx) error {
							if string(tx.Get(s.uidKey(ref, "item"))) != "item" {
								t.Error("rejected delete changed the UID index")
							}
							return nil
						})
					}
				})
			}
		})
	}
}

func TestConditionalDeleteRespectsSharedPathsAndPermissions(t *testing.T) {
	const owner, guest = "owner@example.org", "guest@example.org"
	for _, kind := range []Kind{Calendar, AddressBook} {
		t.Run(string(kind), func(t *testing.T) {
			for _, test := range []struct {
				name               string
				write, own, revoke bool
				want               int
			}{
				{"read only", false, false, false, http.StatusForbidden},
				{"owner path hidden", true, true, false, http.StatusNotFound},
				{"shared path writable", true, false, false, http.StatusNoContent},
				{"share revoked", true, false, true, http.StatusNotFound},
			} {
				t.Run(test.name, func(t *testing.T) {
					s := open(t)
					ref := Ref{Owner: owner, ID: "work"}
					if _, err := s.Create(Collection{Owner: owner, ID: ref.ID, Kind: kind}); err != nil {
						t.Fatal(err)
					}
					if _, err := s.PutObject(ref, "item", "item", []byte("held"), nil, owner); err != nil {
						t.Fatal(err)
					}
					if err := s.PutShare(Share{Owner: owner, Collection: ref.ID, To: guest, Accepted: true, Write: test.write}); err != nil {
						t.Fatal(err)
					}
					if test.revoke {
						writer := *s
						s.db.db = &beforeUpdate{DB: s.db.db, next: func() {
							if err := writer.Answer(ref, guest, false); err != nil {
								t.Fatal(err)
							}
						}}
					}
					path := HomePath(kind, guest) + "work" + aliasSep + owner + "/item"
					if test.own {
						path = HomePath(kind, owner) + "work/item"
					}
					r := httptest.NewRequest(http.MethodDelete, path, nil)
					r.Header.Set("If-Match", `"`+ETagOf([]byte("held"))+`"`)
					r.Header.Set("If-None-Match", `"other"`)
					w := httptest.NewRecorder()
					s.Handler().ServeHTTP(w, r.WithContext(As(r.Context(), guest)))
					if w.Code != test.want {
						t.Fatalf("DELETE: got %d, want %d: %s", w.Code, test.want, w.Body.String())
					}
					if test.want != http.StatusNoContent {
						if _, err := s.Object(ref, "item"); err != nil {
							t.Fatalf("rejected delete lost the object: %v", err)
						}
					}
				})
			}
		})
	}
}
