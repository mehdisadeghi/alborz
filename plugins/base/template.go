package alpsbase

import (
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/emersion/go-imap/v2"
)

const (
	inputDateLayout = "2006-01-02"
	inputTimeLayout = "15:04"
)

var templateFuncs = template.FuncMap{
	"tuple": func(values ...interface{}) []interface{} {
		return values
	},
	"pathescape": url.PathEscape,
	"formatdate": func(t time.Time) string {
		return t.Format("Mon Jan 02 15:04")
	},
	"formatflag": func(flag imap.Flag) string {
		switch flag {
		case imap.FlagSeen:
			return "Seen"
		case imap.FlagAnswered:
			return "Answered"
		case imap.FlagFlagged:
			return "Starred"
		case imap.FlagDraft:
			return "Draft"
		default:
			return string(flag)
		}
	},
	"ismutableflag": func(flag imap.Flag) bool {
		switch flag {
		case imap.FlagAnswered, imap.FlagDeleted, imap.FlagDraft:
			return false
		default:
			return true
		}
	},
	"join": strings.Join,
	"formatinputdate": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format(inputDateLayout)
	},
	"formatinputtime": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format(inputTimeLayout)
	},
	"humantime": humanize.Time,
}
