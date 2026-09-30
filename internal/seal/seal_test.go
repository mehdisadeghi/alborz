package seal

import (
	"bytes"
	"errors"
	"testing"
)

func root(t *testing.T, text string) Root {
	t.Helper()
	r, err := ParseRoot(text)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const (
	one = "YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA="
	two = "q2cV8ZlWmQ2FQm8m2JbM0m9m8p0v1l9c3Qq0a5pX0kU="
)

func TestSealOpensOnlyUnderItsKey(t *testing.T) {
	plain := []byte("a@test.local")
	sealed := root(t, one).Key("sessions").Seal(plain)
	if bytes.Contains(sealed, plain) {
		t.Fatal("the sealed value holds the plain text")
	}
	got, err := root(t, one).Key("sessions").Open(sealed)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("Open = %q, %v", got, err)
	}
	for name, k := range map[string]*Key{
		"another login key": root(t, two).Key("sessions"),
		"another purpose":   root(t, one).Key("settings"),
	} {
		if _, err := k.Open(sealed); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: Open = %v, want ErrOpen", name, err)
		}
	}
	changed := bytes.Clone(sealed)
	changed[len(changed)-1] ^= 1
	if _, err := root(t, one).Key("sessions").Open(changed); !errors.Is(err, ErrOpen) {
		t.Errorf("a changed value: Open = %v, want ErrOpen", err)
	}
}

func TestFernetIsAnotherFormat(t *testing.T) {
	token := []byte("gAAAAABnAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==")
	if _, err := root(t, one).Key("sessions").Open(token); !errors.Is(err, ErrFormat) {
		t.Fatalf("Open = %v, want ErrFormat", err)
	}
}

func TestNamesDifferPerKey(t *testing.T) {
	a, b := root(t, one).Names(), root(t, two).Names()
	name := a.Of("a@test.local")
	if name != a.Of("a@test.local") {
		t.Fatal("a name changes between calls")
	}
	if name == b.Of("a@test.local") || name == a.Of("b@test.local") {
		t.Fatal("two keys or two values give one name")
	}
	if len(name) != 32 {
		t.Fatalf("name %q is not 16 bytes in hex", name)
	}
}

func TestParseRootRefusesShortKeys(t *testing.T) {
	if _, err := ParseRoot("c2hvcnQ="); err == nil {
		t.Fatal("a 5-byte key was taken")
	}
}

// A value tells the kind of key that sealed it, and one kind never opens the other's.
func TestFormatTellsTheKindOfKey(t *testing.T) {
	var secret [KeySize]byte
	copy(secret[:], "an account's own key, 32 bytes!!")
	own, instance := AccountRoot(secret).Key("settings"), NewKey(secret)
	sealed := own.Seal([]byte("a@test.local"))
	if Format(sealed) != ByAccount || Format(instance.Seal(nil)) != ByInstance {
		t.Fatalf("formats %d and %d", Format(sealed), Format(instance.Seal(nil)))
	}
	if _, err := own.Open(sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := AccountRoot(secret).Key("mirror").Open(sealed); !errors.Is(err, ErrOpen) {
		t.Errorf("another purpose: Open = %v, want ErrOpen", err)
	}
	// The seal covers the byte: the same secret as an instance's key does not open it.
	flipped := NewAccountKey(secret).Seal([]byte("a@test.local"))
	flipped[0] = ByInstance
	if _, err := instance.Open(flipped); !errors.Is(err, ErrOpen) {
		t.Errorf("the byte changed: Open = %v, want ErrOpen", err)
	}
}
