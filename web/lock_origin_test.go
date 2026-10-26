package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"git.mehdix.org/alborz"
	"github.com/go-webauthn/webauthn/protocol"
)

func TestPasskeysValidateTheConfiguredPublicOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, request, public, forwarded, origin, id string
		trusted                                      bool
	}{
		{"public scheme", "http://mail.example.org", "https://mail.example.org", "", "https://mail.example.org", "mail.example.org", false},
		{"public HTTPS behind HTTP", "http://backend.internal:1323", "https://mail.example.org", "", "https://mail.example.org", "mail.example.org", false},
		{"public port", "http://backend.internal:1323", "https://mail.example.org:8443", "", "https://mail.example.org:8443", "mail.example.org", false},
		{"direct TLS", "https://mail.example.org", "", "", "https://mail.example.org", "mail.example.org", false},
		{"trusted proxy", "http://mail.example.org", "", "https", "https://mail.example.org", "mail.example.org", true},
		{"untrusted forwarded header", "http://mail.example.org", "", "https", "http://mail.example.org", "mail.example.org", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.request+"/settings/passkeys/begin", nil)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("X-Forwarded-Proto", tc.forwarded)
			req.Header.Set("Origin", "https://untrusted.example")
			server := &Server{Server: &alborz.Server{}}
			if tc.public != "" {
				server.PublicURL, _ = url.Parse(tc.public)
			}
			if tc.trusted {
				server.trusted = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
			}
			session, _ := newSession()
			ctx := &Context{Server: server, request: req, session: session, accountsLoaded: true}
			w, _, err := ctx.passkeys()
			if err != nil {
				t.Fatal(err)
			}
			if w.Config.RPID != tc.id {
				t.Fatalf("RP ID = %q, want %q", w.Config.RPID, tc.id)
			}
			for _, ceremony := range []protocol.CeremonyType{protocol.CreateCeremony, protocol.AssertCeremony} {
				client := protocol.CollectedClientData{Type: ceremony, Challenge: "challenge", Origin: tc.origin}
				verify := func() error {
					return client.Verify("challenge", ceremony, w.Config.RPOrigins, nil, nil, protocol.TopOriginExplicitVerificationMode, false)
				}
				if err := verify(); err != nil {
					t.Fatalf("browser's public origin rejected: %v", err)
				}
				for _, invalid := range []string{"https://untrusted.example", "https://mail.example.org:9443"} {
					client.Origin = invalid
					if err := verify(); err == nil {
						t.Fatalf("accepted unrelated origin %q", invalid)
					}
				}
			}
		})
	}
}

func TestPasskeyOriginFailureLogsOnlyOriginDiagnostics(t *testing.T) {
	server := rememberedServer(t, nil)
	var logged bytes.Buffer
	server.Logger().SetOutput(&logged)
	for _, tc := range []struct {
		details, info string
		wantInfo      bool
	}{
		{"Error validating origin", "Expected Values: [https://mail.example.org], Received: http://mail.example.org", true},
		{"Error validating challenge", "a-private-challenge", false},
	} {
		logged.Reset()
		out := httptest.NewRecorder()
		ctx := &Context{Server: server, response: &Response{ResponseWriter: out}}
		err := protocol.ErrVerification.WithDetails(tc.details).WithInfo(tc.info)
		if err := PasskeyFailure(ctx, http.StatusUnprocessableEntity, "passkey refused", "The passkey was not added.", err); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logged.String(), tc.info) != tc.wantInfo {
			t.Fatalf("wrong diagnostic visibility: %q", logged.String())
		}
		if strings.Contains(out.Body.String(), tc.info) || out.Code != http.StatusUnprocessableEntity {
			t.Fatalf("failure response leaked diagnostics: %s", out.Body.String())
		}
	}
}
