//go:build !nomotd && !noaddons

package web_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func collection(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheTextOfTheDayIsInTheReadersLanguage(t *testing.T) {
	dir := t.TempDir()
	collection(t, dir, "en.toml", "title = \"Quatrains\"\nauthor = \"Khayyam\"\nlanguage = \"en\"\n"+
		"[[texts]]\nnumber = 1\nurl = \"https://example.org/1\"\ntext = \"\"\"\nFirst line\nSecond line\"\"\"\n")
	collection(t, dir, "fa.toml", "author = \"خیام\"\nlanguage = \"fa\"\n[[texts]]\nnumber = 1\ntext = \"یک بیت\"\n")
	a := Alborz(t, MOTD(dir))
	c := a.SignIn(t, User)

	shown := c.Page(t, "core.status", "motd")
	for _, want := range []string{"First line / Second line", "Khayyam", `href="https://example.org/1"`, "Quatrains"} {
		if !strings.Contains(shown, want) {
			t.Errorf("the English text does not show %q:\n%s", want, shown)
		}
	}
	req, err := http.NewRequest(http.MethodGet, a.URL+a.Path("core.status", "motd"), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Language", "fa")
	if shown := c.Do(t, req).Text; !strings.Contains(shown, "یک بیت") {
		t.Errorf("a Persian reader is not shown the Persian text:\n%s", shown)
	}
}
