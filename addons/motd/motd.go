// Package motd is a message of the day: one short text a day, in the reader's own language, from
// collections a deployment supplies. Nothing ships with alborz - the texts are somebody's choice
// and somebody's licence - so the add-on exists only where a deployment names a directory of them.
// example.toml beside this file is the whole of the format.
package motd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// Collection is one book of short texts, in one language.
type Collection struct {
	Title         string
	Author        string
	Translator    string
	TranslatorURL string `toml:"translator_url"`
	Source        string
	SourceURL     string `toml:"source_url"`
	License       string
	Language      string
	Texts         []Text
}

// Text is one of them; Number is what the book calls it, URL where it points of its own.
type Text struct {
	Number int
	URL    string
	Text   string
}

// Day is the day's text as a page shows it, and whom it names.
type Day struct {
	Text   string
	Title  string
	Author string
	// Credit names whoever the collection says to name besides the author, a translator before a
	// source, and CreditURL is where they say to point.
	Credit    string
	CreditURL string
	Language  string
}

// Collections are a deployment's collections by the language each declares.
type Collections map[string]Collection

// Read reads every collection in dir. A deployment that named the directory asked for the texts,
// so one it cannot read is an error, not a day without a text.
func Read(dir string) (Collections, error) {
	// Globbing a directory that is not there finds nothing, which would say the wrong thing.
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s holds no .toml collections", dir)
	}
	out := Collections{}
	for _, name := range names {
		var c Collection
		if _, err := toml.DecodeFile(name, &c); err != nil {
			return nil, err
		}
		switch {
		case c.Language == "":
			return nil, fmt.Errorf("%s names no language", name)
		case len(c.Texts) == 0:
			return nil, fmt.Errorf("%s holds no texts", name)
		}
		if _, ok := out[c.Language]; ok {
			return nil, fmt.Errorf("%s is a second collection in %q", name, c.Language)
		}
		out[c.Language] = c
	}
	return out, nil
}

// fallback is the language a reader without a collection of their own is shown, then any there is:
// better a text they may not read than nothing where the deployment asked for something.
const fallback = "en"

// On is the text for a reader of lang on the day of t, which picks it: everyone reading one
// deployment meets the same one, and it changes at their midnight.
func (cs Collections) On(lang string, t time.Time) Day {
	c, ok := cs[lang]
	if !ok {
		c, ok = cs[fallback]
	}
	if !ok {
		for _, any := range cs {
			c = any
			break
		}
	}
	y, m, d := t.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / int64(24*time.Hour/time.Second)
	text := c.Texts[int(day%int64(len(c.Texts)))]
	out := Day{Text: text.Text, Title: c.Title, Author: c.Author, Credit: c.Translator, CreditURL: c.TranslatorURL, Language: c.Language}
	if out.Credit == "" {
		out.Credit, out.CreditURL = c.Source, c.SourceURL
	}
	if text.URL != "" {
		out.CreditURL = text.URL
	}
	return out
}
