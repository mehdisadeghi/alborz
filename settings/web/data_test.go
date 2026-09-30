package web_test

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

const card = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:ada\r\nFN:Ada Lovelace\r\nN:Lovelace;Ada;;;\r\nEMAIL:ada@example.org\r\nEND:VCARD\r\n"

func unpack(t *testing.T, archive string) map[string][]byte {
	t.Helper()
	z, err := gzip.NewReader(strings.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	tr := tar.NewReader(z)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		if files[h.Name], err = io.ReadAll(tr); err != nil {
			t.Fatal(err)
		}
	}
}

// comparable drops what an import moves on purpose: the ctag, so a client looks again, and when and where
// the archive was made.
func comparable(t *testing.T, files map[string][]byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, data := range files {
		switch {
		case name == "alborz.json":
			continue
		case strings.HasSuffix(name, "/collection.json"):
			var record map[string]any
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			delete(record["Collection"].(map[string]any), "CTag")
			data, _ = json.Marshal(record)
		}
		out[name] = string(data)
	}
	return out
}

func exported(t *testing.T, c *Client) string {
	t.Helper()
	resp := c.Get(t, "settings.data.export", url.Values{"account": {User}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export answered %s", resp.Status)
	}
	return resp.Text
}

// An archive carries an account to another alborz, with its own mail server and login key (ADR 27).
func TestAnArchiveReproducesTheAccountOnAnotherAlborz(t *testing.T) {
	first := Alborz(t)
	c := first.SignIn(t, User)
	if resp := c.Post(t, "settings.profile", url.Values{"messages_per_page": {"17"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the profile: %s", resp.Status)
	}
	cal := c.CreateCalendar(t, "Personal", "")
	first.Put(t, cal+"review.ics", "text/calendar", Event("review", "Budget review"))
	book := c.CreateBook(t, "Friends")
	first.Put(t, book+"ada.vcf", "text/vcard", card)

	raw := exported(t, c)
	archive := unpack(t, raw)
	var ics, vcf bool
	for name, data := range archive {
		ics = ics || strings.HasSuffix(name, "/review.ics") && strings.Contains(string(data), "SUMMARY:Budget review")
		vcf = vcf || strings.HasSuffix(name, "/ada.vcf") && string(data) == card
		if strings.HasPrefix(name, "settings/http-password") {
			t.Errorf("the archive holds %s", name)
		}
	}
	if !ics || !vcf || archive["settings/profile.json"] == nil {
		t.Fatalf("the archive lacks the event, the card or the profile: %d files", len(archive))
	}

	second := Alborz(t, LoginKey("q2cV8ZlWmQ2FQm8m2JbM0m9m8p0v1l9c3Qq0a5pX0kU="))
	d := second.SignIn(t, User)
	resp := d.Upload(t, "settings.data", nil, []File{{Field: "file", Name: "alborz.tar.gz", Body: strings.NewReader(raw)}},
		url.Values{"account": {User}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("import answered %s", resp.Status)
	}

	got, want := comparable(t, unpack(t, exported(t, d))), comparable(t, archive)
	for name, data := range want {
		if got[name] != data {
			t.Errorf("%s after the import:\n%s\nwant\n%s", name, got[name], data)
		}
	}
	if second.DAV(t, http.MethodGet, cal+"review.ics", nil, "").Text != Event("review", "Budget review") {
		t.Error("the event does not read back over DAV")
	}
	if got := perPage(t, second.SignIn(t, User)); got != "17" {
		t.Errorf("the imported profile holds a page size of %s, want 17", got)
	}
}

func TestAFileThatIsNoArchiveIsRefused(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	resp := c.Upload(t, "settings.data", nil, []File{{Field: "file", Name: "notes.tar.gz", Body: strings.NewReader("notes")}},
		url.Values{"account": {User}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(resp.Text, "not an alborz archive") {
		t.Fatalf("a file that is no archive answered %s", resp.Status)
	}
}
