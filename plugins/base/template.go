package alborzbase

import (
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
)

const (
	inputDateLayout     = "2006-01-02"
	inputTimeLayout     = "15:04"
	inputDateTimeLayout = "2006-01-02T15:04"
)

var templateFuncs = template.FuncMap{
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
	"humansize": func(n int64) string {
		switch {
		case n >= 1<<20:
			return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
		case n >= 1<<10:
			return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
		default:
			return fmt.Sprintf("%d B", n)
		}
	},
	"tuple": func(values ...interface{}) []interface{} {
		return values
	},
	// Named arguments for shared defines; positional tuples stop scaling
	// past a few fields.
	"dict": func(pairs ...interface{}) (map[string]interface{}, error) {
		if len(pairs)%2 != 0 {
			return nil, fmt.Errorf("dict: odd number of arguments")
		}
		m := make(map[string]interface{}, len(pairs)/2)
		for i := 0; i < len(pairs); i += 2 {
			k, ok := pairs[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict: key %v is not a string", pairs[i])
			}
			m[k] = pairs[i+1]
		}
		return m, nil
	},
	"pathescape": url.PathEscape,
	// An address in a query value keeps its at sign; see AccountParam.
	"accountparam": alborz.AccountParam,
	// account renders the query fragment naming an account, or nothing
	// when there is none. The template.URL return matters: the contextual
	// escaper normalises those instead of escaping them, so the address
	// keeps its at sign inside an href or an action.
	"account": func(sep, username string) template.URL {
		if username == "" {
			return ""
		}
		return template.URL(sep + "account=" + alborz.AccountParam(username))
	},
	// localhref preserves the query separators in application-generated
	// relative links. html/template otherwise treats a complete dynamic
	// href as one query value and percent-escapes '&' and '='. Only local
	// absolute paths, query strings, and fragments are accepted.
	"localhref": func(raw string) template.URL {
		if raw == "" {
			return ""
		}
		if strings.HasPrefix(raw, "//") || (raw[0] != '/' && raw[0] != '?' && raw[0] != '#') {
			return "#"
		}
		if _, err := url.Parse(raw); err != nil {
			return "#"
		}
		return template.URL(raw)
	},
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
}
