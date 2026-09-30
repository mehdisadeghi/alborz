package web

import (
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/lang"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
)

// TemplateFuncs are the functions the templates call.
var TemplateFuncs = template.FuncMap{
	// Here, not in carddav: the templates reference it and it must exist at parse time.
	"photodatauri": func(data string) template.URL {
		if data == "" {
			return ""
		}
		// Remote photo URLs would leak the viewer's address; show nothing.
		if strings.HasPrefix(data, "http://") || strings.HasPrefix(data, "https://") {
			return ""
		}
		if strings.HasPrefix(data, "data:") {
			return template.URL(data)
		}
		return template.URL("data:image/jpeg;base64," + data)
	},
	"humansize": mail.FormatSize,
	"partquery": partQuery,
	"sub":       func(a, b int) int { return a - b },
	// A theme's name is a proper noun, so it is not a locale string.
	"themename": web.ThemeName,
	// A crumb step may already carry a query.
	"qsep": func(raw string) string {
		if strings.Contains(raw, "?") {
			return "&"
		}
		return "?"
	},
	"hrefaccount": func(href string) string {
		u, err := url.Parse(href)
		if err != nil {
			return ""
		}
		return u.Query().Get("account")
	},
	"from": func(href, list string) string {
		if list == "" {
			return href
		}
		sep := "?"
		if strings.Contains(href, "?") {
			sep = "&"
		}
		return href + sep + "from=" + web.QueryValue(list)
	},
	"mod": func(a, b int) int { return a % b },
	// PARTSTAT arrives upper case and the locale keys are not.
	"lower": strings.ToLower,
	// So a template can ask whether a date is set without the caller passing a zero time.
	"zerotime": func() time.Time { return time.Time{} },
	"tuple": func(values ...interface{}) []interface{} {
		return values
	},
	// Takes the render data for its translator: a method value cannot be passed through a dict.
	"flagcolors": func(t interface{ T(string) string }) []mail.FlagColorOption {
		opts := make([]mail.FlagColorOption, len(alborz.FlagColors))
		for i, name := range alborz.FlagColors {
			opts[i] = mail.FlagColorOption{Name: name, Label: t.T("color." + name)}
		}
		return opts
	},
	"devicename": web.DeviceName,
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
	"sortaria": func(explicit bool, sort, key, dir string) string {
		if !explicit || sort != key {
			return "none"
		}
		if dir == "desc" {
			return "descending"
		}
		return "ascending"
	},
	// A colon escaped too, or a segment after a dynamic prefix reads to the escaper as a scheme: a DAV path's
	// marker carries a host's port (ADR 28).
	"pathescape": func(s string) string { return strings.ReplaceAll(url.PathEscape(s), ":", "%3A") },
	// template.URL, or the escaper would percent-encode what QueryValue left alone.
	"qv":         func(v string) template.URL { return template.URL(web.QueryValue(v)) },
	"subjectdir": lang.SubjectDir,
	"account":    web.AccountParam,
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
	"flagkey": func(flag imap.Flag) string {
		switch flag {
		case imap.FlagSeen:
			return "flag.seen"
		case imap.FlagAnswered:
			return "flag.answered"
		case imap.FlagFlagged:
			return "flag.starred"
		case imap.FlagDraft:
			return "flag.draft"
		default:
			return ""
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
}

// partQuery picks the part out of its message's page or download; without the account the first signed-in one serves it.
func partQuery(node mail.IMAPPartNode, account string) string {
	q := url.Values{"part": {node.PathString()}}
	if account != "" {
		q.Set("account", account)
	}
	return "?" + web.Query(q)
}
