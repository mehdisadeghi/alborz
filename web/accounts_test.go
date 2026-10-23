package web

import "testing"

func TestLocalPathRejectsBrowserAuthorityAndSchemes(t *testing.T) {
	for _, raw := range []string{
		"https://evil.example/path", "//evil.example/path",
		"http:/evil.example/path", "https:/evil.example/path", "javascript:/a/path",
		"///evil.example/path", "////evil.example/path",
		"/%2Fevil.example/path", "/%2f%2fevil.example/path", "%2F%2Fevil.example/path",
		`/\evil.example/path`, "relative/path", "",
	} {
		t.Run(raw, func(t *testing.T) {
			if got := localPath(raw); got != "" {
				t.Fatalf("accepted non-local return path %q as %q", raw, got)
			}
		})
	}
	for _, raw := range []string{"/", "/mail/INBOX", "/safe?next=//evil.example/path", "/a%20b#part"} {
		if got := localPath(raw); got != raw {
			t.Errorf("local path %q became %q", raw, got)
		}
	}
}
