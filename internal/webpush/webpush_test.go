package webpush

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// RFC 8291 Appendix A.
const (
	rfcPlaintext = "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcMessage   = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func decode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := b64.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func privateKey(t *testing.T, s string) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.P256().NewPrivateKey(decode(t, s))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestEncryptMatchesRFC8291(t *testing.T) {
	sub := Subscription{P256dh: decode(t, rfcUAPublic), Auth: decode(t, rfcAuth)}
	got, err := encrypt(sub, decode(t, rfcPlaintext), privateKey(t, rfcASPrivate), decode(t, rfcSalt))
	if err != nil {
		t.Fatal(err)
	}
	if want := decode(t, rfcMessage); !bytes.Equal(got, want) {
		t.Fatalf("encrypted\n%s\nwant\n%s", b64.EncodeToString(got), rfcMessage)
	}
}

func TestDecryptOpensRFC8291(t *testing.T) {
	got, err := Decrypt(privateKey(t, rfcUAPrivate), decode(t, rfcAuth), decode(t, rfcMessage))
	if err != nil {
		t.Fatal(err)
	}
	if want := decode(t, rfcPlaintext); !bytes.Equal(got, want) {
		t.Fatalf("decrypted %q, want %q", got, want)
	}
}

func TestSendIsSignedAndOpensForTheBrowserOnly(t *testing.T) {
	ua := privateKey(t, rfcUAPrivate)
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	var header http.Header
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		header = r.Header
		w.WriteHeader(http.StatusCreated)
	}))
	defer service.Close()
	sub := Subscription{Endpoint: service.URL + "/push/1", P256dh: ua.PublicKey().Bytes(), Auth: decode(t, rfcAuth)}
	status, err := Send(context.Background(), service.Client(), key, "mailto:ops@example.org", sub,
		Message{Payload: []byte(`{"title":"New mail"}`), TTL: time.Hour, Topic: "mail"})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("send: %d %v", status, err)
	}
	if bytes.Contains(body, []byte("New mail")) {
		t.Fatal("the payload travels in the clear")
	}
	plain, err := Decrypt(ua, sub.Auth, body)
	if err != nil || string(plain) != `{"title":"New mail"}` {
		t.Fatalf("the browser reads %q, %v", plain, err)
	}
	if _, err := Decrypt(privateKey(t, rfcASPrivate), sub.Auth, body); err == nil {
		t.Fatal("another key opens the payload")
	}
	public, claims, err := Verify(header.Get("Authorization"))
	if err != nil || public != key.Public() {
		t.Fatalf("vapid: %q %v", public, err)
	}
	if claims["aud"] != service.URL || claims["sub"] != "mailto:ops@example.org" {
		t.Fatalf("claims %v", claims)
	}
	if header.Get("Content-Encoding") != "aes128gcm" || header.Get("TTL") != "3600" || header.Get("Topic") != "mail" {
		t.Fatalf("headers %v", header)
	}
}

func TestKeyReadsBackAsItself(t *testing.T) {
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseKey(key.Bytes())
	if err != nil || again.Public() != key.Public() {
		t.Fatalf("the key read back is another: %v", err)
	}
	if raw := decode(t, key.Public()); len(raw) != 65 || raw[0] != 4 {
		t.Fatalf("the public key is not an uncompressed point: %d bytes", len(raw))
	}
}

func TestTooLongAPayloadIsRefused(t *testing.T) {
	sub := Subscription{P256dh: decode(t, rfcUAPublic), Auth: decode(t, rfcAuth)}
	if _, err := Encrypt(sub, []byte(strings.Repeat("x", MaxPayload+1))); err == nil {
		t.Fatal("a payload over one record was sealed")
	}
	if _, err := Encrypt(sub, []byte(strings.Repeat("x", MaxPayload))); err != nil {
		t.Fatal(err)
	}
}

func TestParseTakesOnlyAnHTTPSEndpointAndSoundKeys(t *testing.T) {
	if _, err := Parse("https://push.example.org/1", rfcUAPublic, rfcAuth); err != nil {
		t.Fatal(err)
	}
	for name, sub := range map[string][3]string{
		"http":        {"http://push.example.org/1", rfcUAPublic, rfcAuth},
		"not a point": {"https://push.example.org/1", rfcAuth, rfcAuth},
		"short auth":  {"https://push.example.org/1", rfcUAPublic, "BTBZ"},
	} {
		if _, err := Parse(sub[0], sub[1], sub[2]); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
}
