package record

import (
	"encoding/json"
	"testing"
)

// What a newer version stores, and what an older one knows of it.
type newer struct {
	Name    string
	Count   uint64
	Added   string `json:",omitempty"`
	Inner   newerInner
	Entries []string
}

type newerInner struct {
	Kept  bool
	Added string `json:",omitempty"`
}

type older struct {
	Name    string
	Count   uint64
	Inner   olderInner
	Entries []string
}

type olderInner struct {
	Kept bool
}

func TestAnOlderVersionHandsBackWhatANewerOneWrote(t *testing.T) {
	stored, err := json.Marshal(newer{
		Name: "before", Count: 1 << 60, Added: "new top", Inner: newerInner{Kept: true, Added: "new inside"},
		Entries: []string{"a", "b"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var old older
	if err := json.Unmarshal(stored, &old); err != nil {
		t.Fatal(err)
	}
	old.Name, old.Inner.Kept, old.Entries = "after", false, []string{"c"}
	written, err := Keep(stored, &old)
	if err != nil {
		t.Fatal(err)
	}

	var back newer
	if err := json.Unmarshal(written, &back); err != nil {
		t.Fatal(err)
	}
	want := newer{
		Name: "after", Count: 1 << 60, Added: "new top", Inner: newerInner{Kept: false, Added: "new inside"},
		Entries: []string{"c"},
	}
	if back.Name != want.Name || back.Count != want.Count || back.Added != want.Added ||
		back.Inner != want.Inner || len(back.Entries) != 1 || back.Entries[0] != "c" {
		t.Fatalf("written back:\n got %+v\nwant %+v\nfrom %s", back, want, written)
	}
}

func TestAnEntryThisVersionDeletedStaysDeleted(t *testing.T) {
	type entries struct {
		Entries map[string]olderInner
	}
	stored := []byte(`{"Entries":{"gone":{"Kept":true,"Added":"new"},"stays":{"Kept":true,"Added":"new"}}}`)
	written, err := Keep(stored, &entries{Entries: map[string]olderInner{"stays": {Kept: false}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"Entries":{"stays":{"Added":"new","Kept":false}}}`; string(written) != want {
		t.Fatalf("got %s, want %s", written, want)
	}
}

func TestAFirstWriteIsTheValueAlone(t *testing.T) {
	written, err := Keep(nil, &older{Name: "only"})
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := json.Marshal(older{Name: "only"}); string(written) != string(want) {
		t.Fatalf("got %s, want %s", written, want)
	}
}
