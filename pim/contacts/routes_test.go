package contacts

import (
	"context"
	"git.mehdix.org/alborz/internal/keyring"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/internal/seal"
	"git.mehdix.org/alborz/pim/store"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

func TestWithValuesKeepsTheParametersOfEditedFields(t *testing.T) {
	work := &vcard.Field{Value: "old@example.org", Params: vcard.Params{vcard.ParamType: {"work"}}}
	got := WithValues([]*vcard.Field{work}, []string{" new@example.org ", "second@example.org", ""})
	if len(got) != 2 {
		t.Fatalf("got %d fields, want 2", len(got))
	}
	if got[0].Value != "new@example.org" || got[0].Params.Get(vcard.ParamType) != "work" {
		t.Errorf("the first address lost its parameters: %v %v", got[0].Value, got[0].Params)
	}
	if got[1].Value != "second@example.org" || len(got[1].Params) != 0 {
		t.Errorf("a new address carries parameters it was never given: %v", got[1].Params)
	}
	if got := WithValues([]*vcard.Field{work}, []string{" "}); got != nil {
		t.Errorf("blank input kept a field: %v", got)
	}
}

func TestAUIDIsMatchedExactlyAndANameRegardlessOfCase(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "alborz.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	root, err := seal.ParseRoot("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")
	if err != nil {
		t.Fatal(err)
	}
	const account = "u@example.org"
	ring, err := keyring.New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	h := store.New(db, root, ring).Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(store.As(r.Context(), account)))
	}))
	t.Cleanup(srv.Close)
	client, err := carddav.NewClient(srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	book := store.HomePath(store.AddressBook, account) + "b/"
	if err := client.CreateAddressBook(ctx, &carddav.AddressBook{Path: book}); err != nil {
		t.Fatal(err)
	}
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, "Card-1")
	card.SetValue(vcard.FieldFormattedName, "Alice Example")
	if _, err := client.PutAddressObject(ctx, book+"c.vcf", card, nil); err != nil {
		t.Fatal(err)
	}

	if got := cardPathByUID(ctx, client, book, "Card-1"); got != book+"c.vcf" {
		t.Errorf("the UID found %q, want %q", got, book+"c.vcf")
	}
	if got := cardPathByUID(ctx, client, book, "card-1"); got != "" {
		t.Errorf("a UID differing in case found %q", got)
	}
	query := Query("ALICE")
	found, err := client.QueryAddressBook(ctx, book, &query)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Errorf("a search in another case found %d cards, want 1", len(found))
	}
}
