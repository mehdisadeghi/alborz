package web

import (
	"errors"
	"fmt"
	"html/template"
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
// the service as a reader calls it, and troubles holds what kept each
// account's server away when the caller knows it, for its host and why; a
// host it does not name falls back to the account's mail server's host.
func (ctx *Context) Unreachable(kind string, accounts []string, troubles map[string]error) {
	// What an action said is never replaced by what a page came across.
	if len(accounts) == 0 || ctx.NoticeWaiting() {
		return
	}
	type said struct{ name, where string }
	var told []said
	for _, name := range accounts {
		if !ctx.Session().OnceFor("unreachable:"+name, NoticeCooldown) {
			continue
		}
		down, _ := errors.AsType[alborz.UpstreamError](troubles[name])
		at := down.Host
		if at == "" {
			at = ctx.upstreamHost("mail", name)
		}
		told = append(told, said{name, ctx.where(at, down.Why())})
	}
	if len(told) == 0 {
		return
	}
	shown := make([]string, len(told))
	linked := make([]string, len(told))
	for i, s := range told {
		shown[i] = isolated(s.name) + s.where
		linked[i] = isolated(NoticeLink(s.name, ctx.Path("mail.account")+"?account="+QueryValue(s.name))) + template.HTMLEscapeString(s.where)
	}
	ctx.NotifyBackground(Notice{Kind: NoticeWarning,
		Text:   ctx.Tf("notice.unreachable", len(told), kind, strings.Join(shown, ", ")),
		Markup: template.HTML(ctx.Tf("notice.unreachable", len(told), kind, strings.Join(linked, ", ")))})
}

// upstreamHost names the host of the account's server for service, as the
// server's upstreams hold it; "" when it is not known.
func (ctx *Context) upstreamHost(service, account string) string {
	_, domain, _ := strings.Cut(account, "@")
	upstreams := ctx.Server.UpstreamsFor(domain)
	return map[string]string{"mail": upstreams.IMAP, "sieve": upstreams.Sieve}[service]
}

// Unanswered says which server did not answer for which account, where it
// is and why, with its identifiers isolated by character, as a notice's text
// and a plain answer cannot hold a bdi.
func (ctx *Context) Unanswered(err alborz.UpstreamError) string {
	account, host := err.Account, err.Host
	if account == "" && ctx.Login != nil {
		account = ctx.Login.Username()
	}
	if host == "" {
		host = ctx.upstreamHost(err.Service, account)
	}
	return ctx.Tf("upstream.noanswer", ctx.T("upstream."+err.Service), isolated(account)+ctx.where(host, err.Why()))
}

// where follows an account whose server did not answer: the server's host,
// and why it did not answer where that is known.
func (ctx *Context) where(host, why string) string {
	if why != "" {
		why = ctx.T("upstream." + why)
	}
	switch {
	case host != "" && why != "":
		return " (" + isolated(host) + ": " + why + ")"
	case host != "":
		return " (" + isolated(host) + ")"
	case why != "":
		return " (" + why + ")"
	}
	return ""
}

func isolated(s string) string { return "\u2066" + s + "\u2069" }

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
