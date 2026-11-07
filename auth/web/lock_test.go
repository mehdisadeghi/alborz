package web_test

import (
	"bytes"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/fxamacker/cbor/v2"
)

func TestBoostedSignInLoadsTheSessionScopedHead(t *testing.T) {
	a := Alborz(t)
	c := a.Client()
	form := url.Values{"username": {User}, "password": {Password}}
	req, _ := http.NewRequest(http.MethodPost, a.URL+a.Path("auth.login"), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Boosted", "true")
	resp := c.Do(t, req)
	to := resp.Header.Get("HX-Redirect")
	if resp.StatusCode != http.StatusOK || to == "" {
		t.Fatalf("sign-in retained its public head: %s %q", resp.Status, to)
	}
	page := c.Follow(t, to)
	cookie := c.Cookie("alborz_session")
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	id, secret, _ := strings.Cut(cookie.Value, ".")
	if !strings.Contains(page, `name="alborz-session" content="`+id+`"`) || !strings.Contains(page, "session-lock.js") {
		t.Fatal("authenticated head lacks session guard")
	}
	if strings.Contains(page, secret) {
		t.Fatal("session guard exposed the cookie secret")
	}
}

func TestSignInRejectsNonLocalReturnPaths(t *testing.T) {
	a := Alborz(t)
	for _, boosted := range []bool{false, true} {
		mode := "native"
		if boosted {
			mode = "boosted"
		}
		t.Run(mode, func(t *testing.T) {
			signIn := func(next string) string {
				t.Helper()
				form := url.Values{"username": {User}, "password": {Password}, "next": {next}}
				req, _ := http.NewRequest(http.MethodPost, a.URL+a.Path("auth.login"), strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				status, header := http.StatusFound, "Location"
				if boosted {
					req.Header.Set("HX-Request", "true")
					req.Header.Set("HX-Boosted", "true")
					status, header = http.StatusOK, "HX-Redirect"
				}
				resp := a.Client().Do(t, req)
				if resp.StatusCode != status {
					t.Fatalf("sign-in: %s", resp.Status)
				}
				return resp.Header.Get(header)
			}
			fallback := signIn("")
			if fallback == "" {
				t.Fatal("sign-in has no default destination")
			}
			for _, next := range []string{"http:/evil.example/path", "javascript:/a/path", "///evil.example/path", "////evil.example/path", "/%2Fevil.example/path"} {
				if got := signIn(next); got != fallback {
					t.Errorf("return path %q redirected to %q instead of %q", next, got, fallback)
				}
			}
		})
	}
}

func TestProtectedSessionCanSignOutWithoutJavaScript(t *testing.T) {
	for _, locked := range []bool{false, true} {
		name := "unlocked"
		if locked {
			name = "locked"
		}
		t.Run(name, func(t *testing.T) {
			a := Alborz(t)
			endpoint, err := url.Parse(a.URL)
			if err != nil {
				t.Fatal(err)
			}
			endpoint.Host = net.JoinHostPort("localhost", endpoint.Port())
			a.URL = endpoint.String()
			c := a.SignIn(t, User)
			enrollFallbackPasskey(t, a, c)
			cookie := c.Cookie("alborz_session")
			id, _, _ := strings.Cut(cookie.Value, ".")
			page := c.Page(t, "settings.profile")
			if !strings.Contains(page, " data-session-concealed") {
				t.Fatal("enrolled private page must remain concealed until verified")
			}
			formArea := Between(page, `<section id="session-lock-screen"`, "</section>")
			if locked {
				c.Post(t, "auth.lock", nil)
				page = c.Page(t, "auth.unlock")
				formArea = Between(page, `<main id="main"`, "</main>")
			}
			form := regexp.MustCompile(`<form method="post" action="([^"]+)"[^>]*>`).FindStringSubmatch(formArea)
			if form == nil || html.UnescapeString(form[1]) != a.Path("auth.unlock.leave") {
				t.Fatal("visible lock screen has no native sign-out form")
			}
			resp := c.PostTo(t, html.UnescapeString(form[1]), nil)
			if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != a.Path("auth.login") {
				t.Fatalf("native sign-out did not redirect to sign-in: %s %q", resp.Status, resp.Header.Get("Location"))
			}
			if c.Cookie("alborz_session") != nil || a.Server.Sessions.Holds(id, User) {
				t.Fatal("sign-out retained the browser session")
			}
			if _, ok := a.Server.SessionRecords.Load(id); ok {
				t.Fatal("sign-out retained the stored passkey session")
			}
			if login := c.Follow(t, resp.Header.Get("Location")); strings.Contains(login, " data-session-concealed") {
				t.Fatal("the sign-in page is concealed after native sign-out")
			}
		})
	}
}

// Enroll through the HTTP ceremony, as a user-verifying authenticator with no attestation.
func enrollFallbackPasskey(t *testing.T, a *App, c *Client) {
	t.Helper()
	begin := c.Post(t, "settings.passkeys.begin", nil)
	var options struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RP        struct {
				ID string `json:"id"`
			} `json:"rp"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal([]byte(begin.Text), &options); err != nil || options.PublicKey.Challenge == "" {
		t.Fatalf("begin registration: %s %v", begin.Text, err)
	}
	private, err := ecdh.P256().NewPrivateKey(append(make([]byte, 31), 1))
	if err != nil {
		t.Fatal(err)
	}
	point := private.PublicKey().Bytes() // 0x04 || X || Y
	key, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: point[1:33], -3: point[33:]})
	if err != nil {
		t.Fatal(err)
	}
	rp := sha256.Sum256([]byte(options.PublicKey.RP.ID))
	auth := append(rp[:], 0x45, 0, 0, 0, 0) // Present, verified, attested credential; counter zero.
	auth = append(auth, make([]byte, 16)...)
	auth = append(auth, 0, 1, 1) // One-byte credential ID.
	auth = append(auth, key...)
	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
	if err != nil {
		t.Fatal(err)
	}
	client, err := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": options.PublicKey.Challenge, "origin": a.URL})
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	credential, err := json.Marshal(map[string]any{"id": "AQ", "rawId": "AQ", "type": "public-key", "response": map[string]string{"clientDataJSON": encode(client), "attestationObject": encode(attestation)}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, a.URL+a.Path("settings.passkeys.finish"), bytes.NewReader(credential))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if resp := c.Do(t, req); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("finish registration: %s %s", resp.Status, resp.Text)
	}
}
