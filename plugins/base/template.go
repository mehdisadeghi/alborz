package alborzbase

import (
	"fmt"
	"hash/fnv"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/emersion/go-imap/v2"
)

const (
	inputDateLayout     = "2006-01-02"
	inputTimeLayout     = "15:04"
	inputDateTimeLayout = "2006-01-02T15:04"
)

var templateFuncs = template.FuncMap{
	// Typed CSS, or html/template poisons the value in a style attribute.
	"accountcolor": func(username string) template.CSS {
		h := fnv.New32a()
		h.Write([]byte(username))
		return template.CSS(fmt.Sprintf("hsl(%d 65%% 45%%)", h.Sum32()%360))
	},
	// Registered here rather than in the carddav plugin because the theme
	// references it and a template function must exist at parse time even
	// when that plugin is absent.
	"photodatauri": func(data string) template.URL {
		if data == "" {
			return ""
		}
		// Remote photo URLs would leak the viewer's address and the
		// CSP blocks them anyway; show nothing instead.
		if strings.HasPrefix(data, "http://") || strings.HasPrefix(data, "https://") {
			return ""
		}
		if strings.HasPrefix(data, "data:") {
			return template.URL(data)
		}
		return template.URL("data:image/jpeg;base64," + data)
	},
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
	"formatinputdatetime": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format(inputDateTimeLayout)
	},
	"humantime": humanize.Time,
}
