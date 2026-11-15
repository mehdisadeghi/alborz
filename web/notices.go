package web

import (
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz"
)

// Notice is what the next page tells the reader about the request that
// redirected to it.
type Notice struct {
	Kind string
	Text string
	// Markup is Text with a link in it; Text is what goes to the log.
	Markup  template.HTML
	Actions []*NoticeAction
}

const (
	NoticeDone    = "done"
	NoticeWarning = "warning"
	NoticeFailed  = "failed"
)

// NoticeAction is a link, or a POST when it changes state.
type NoticeAction struct {
	Label  string
	Path   string
	Fields url.Values
}

// Undo returns an action that POSTs the exact inverse back to path.
func (ctx *Context) Undo(path string, fields url.Values) *NoticeAction {
	fields.Set("undo", "1")
	return &NoticeAction{Label: ctx.T("notice.undo"), Path: path, Fields: fields}
}

// NoticeCooldown is the span a background warning stays quiet in a session,
// long enough that a page's visits do not repeat it, short enough that a
// lasting outage is said again.
const NoticeCooldown = 15 * time.Minute

// Unreachable warns which accounts' servers did not answer, each said once per
// session's notice cooldown, so a page's visits do not repeat it. kind names
// the service as a reader calls it, and hosts names each account's server's
// host when the caller knows it; what it does not know falls back to the
// account's mail server's host.
func (ctx *Context) Unreachable(kind string, accounts []string, hosts map[string]string) {
	// What an action said is never replaced by what a page came across.
	if len(accounts) == 0 || ctx.NoticeWaiting() {
		return
	}
	type said struct{ name, at string }
	var told []said
	for _, name := range accounts {
		if !ctx.Session().OnceFor("unreachable:"+name, NoticeCooldown) {
			continue
		}
		at, known := hosts[name]
		if !known {
			at = ctx.mailHost(name)
		}
		told = append(told, said{name, at})
	}
	if len(told) == 0 {
		return
	}
	shown := make([]string, len(told))
	linked := make([]string, len(told))
	for i, s := range told {
		at := ""
		if s.at != "" {
			at = fmt.Sprintf(" (%s)", s.at)
		}
		shown[i] = s.name + at
		linked[i] = NoticeLink(s.name, ctx.Path("mail.account")+"?account="+QueryValue(s.name)) + at
	}
	ctx.NotifyBackground(Notice{Kind: NoticeWarning,
		Text:   ctx.Tf("notice.unreachable", len(told), kind, strings.Join(shown, ", ")),
		Markup: template.HTML(ctx.Tf("notice.unreachable", len(told), kind, strings.Join(linked, ", ")))})
}

// mailHost names the account's mail server's host, as the server's upstreams
// hold it; "" when it is not known.
func (ctx *Context) mailHost(name string) string {
	login := ctx.LoginFor(name)
	if login == nil {
		return ""
	}
	at := ctx.Server.UpstreamsFor(login.Domain()).IMAP
	if host, _, err := net.SplitHostPort(at); err == nil {
		return host
	}
	return at
}

// NoticeName marks a name inside a notice for the page to quote and truncate.
func NoticeName(name string) string {
	return `<span class="notice-name">` + template.HTMLEscapeString(name) + `</span>`
}

// NoticeLink is NoticeName as a link to href.
func NoticeLink(name, href string) string {
	return `<a class="notice-name" href="` + template.HTMLEscapeString(href) + `">` +
		template.HTMLEscapeString(name) + `</a>`
}

// Made notifies that something was made, linking it and offering another from again, unless empty.
func (ctx *Context) Made(sentence, name, href, again string) {
	// A folder is its own list, so there is nowhere for the name to lead.
	marked := NoticeName(name)
	if href != "" {
		marked = NoticeLink(name, href)
	}
	n := Notice{
		Kind:   NoticeDone,
		Text:   fmt.Sprintf(sentence, name),
		Markup: template.HTML(fmt.Sprintf(sentence, marked)),
	}
	if again != "" {
		n.Actions = []*NoticeAction{{Label: ctx.T("common.addanother"), Path: again}}
	}
	ctx.Notify(n)
}

// SaveRefusal is the alert for a settings write the store did not keep.
func (ctx *Context) SaveRefusal(err error) string {
	if errors.Is(err, alborz.ErrLocked) {
		return ctx.T("form.savelocked")
	}
	return fmt.Sprintf(ctx.T("form.saverefused"), err)
}
