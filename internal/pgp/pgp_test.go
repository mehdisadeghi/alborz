package pgp

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"slices"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/emersion/go-message/textproto"
)

type directory struct {
	asked   []string
	missing string
	answer  func(*http.Request) (*http.Response, error)
}

func (d *directory) RoundTrip(r *http.Request) (*http.Response, error) {
	d.asked = append(d.asked, r.URL.String())
	if r.URL.Hostname() == d.missing {
		return nil, &net.DNSError{Name: d.missing, IsNotFound: true}
	}
	return d.answer(r)
}

func respond(status int, body []byte) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body))}, nil
	}
}

func TestWKD(t *testing.T) {
	const address = "Joe.Doe@Example.ORG"
	// The draft's own example (section 3.1).
	const advanced = "https://openpgpkey.example.org/.well-known/openpgpkey/example.org/hu/iy9q119eutrkn8s1mk4r39qejnbu3n5q?l=Joe.Doe"
	const direct = "https://example.org/.well-known/openpgpkey/hu/iy9q119eutrkn8s1mk4r39qejnbu3n5q?l=Joe.Doe"

	key, err := openpgp.NewEntity("Joe", "", address, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := key.AddUserId("Joe", "", "joe@elsewhere.example", nil); err != nil {
		t.Fatal(err)
	}
	var published bytes.Buffer
	if err := key.Serialize(&published); err != nil {
		t.Fatal(err)
	}

	d := &directory{missing: "openpgpkey.example.org", answer: respond(http.StatusOK, published.Bytes())}
	keys, err := WKD(context.Background(), &http.Client{Transport: d}, address)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys %v, err %v", keys, err)
	}
	if !slices.Equal(d.asked, []string{advanced, direct}) {
		t.Fatalf("asked %q, want the advanced method, then the direct one", d.asked)
	}
	for _, id := range keys[0].Identities {
		if id.UserId.Email != address {
			t.Errorf("kept the User ID %q", id.Name)
		}
	}

	d = &directory{answer: respond(http.StatusNotFound, nil)}
	if keys, err := WKD(context.Background(), &http.Client{Transport: d}, address); err != nil || len(keys) != 0 {
		t.Fatalf("keys %v, err %v, want none", keys, err)
	}
	if !slices.Equal(d.asked, []string{advanced}) {
		t.Fatalf("asked %q, want only the advanced method", d.asked)
	}
}

func TestAutocrypt(t *testing.T) {
	entity, err := openpgp.NewEntity("Gil", "", "gil@example.org", nil)
	if err != nil {
		t.Fatal(err)
	}
	var key bytes.Buffer
	if err := entity.Serialize(&key); err != nil {
		t.Fatal(err)
	}
	keydata := "keydata=" + base64.StdEncoding.EncodeToString(key.Bytes())
	valid := "addr=gil@example.org; prefer-encrypt=mutual; _extra=1; " + keydata

	for _, c := range []struct {
		name    string
		headers []string
		ok      bool
	}{
		{"valid", []string{valid}, true},
		{"one valid beside an invalid", []string{valid, "addr=gil@example.org; " + keydata[:20]}, true},
		{"unknown critical attribute", []string{"addr=gil@example.org; urgent=yes; " + keydata}, false},
		{"no addr", []string{keydata}, false},
		{"addr is not From", []string{"addr=list@example.org; " + keydata}, false},
		{"two valid headers", []string{valid, valid}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var h textproto.Header
			for _, v := range c.headers {
				h.Add("Autocrypt", v)
			}
			keys, err := Autocrypt(h, "gil@example.org")
			if c.ok != (err == nil && len(keys) == 1) {
				t.Fatalf("keys %d, err %v", len(keys), err)
			}
		})
	}
}
