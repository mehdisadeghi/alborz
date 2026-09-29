package web

import (
	"fmt"
	"html/template"
	"net/url"
	"strings"
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

// Unreachable warns which accounts' servers did not answer.
func (ctx *Context) Unreachable(accounts []string) {
	if len(accounts) == 0 {
		return
	}
	linked := make([]string, len(accounts))
	for i, name := range accounts {
		linked[i] = NoticeLink(name, "/settings/account?account="+QueryValue(name))
	}
	ctx.Notify(Notice{Kind: NoticeWarning,
		Text:   ctx.Tf("notice.unreachable", len(accounts), strings.Join(accounts, ", ")),
		Markup: template.HTML(ctx.Tf("notice.unreachable", len(accounts), strings.Join(linked, ", ")))})
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

// Made notifies that something was made, linking it and offering another.
func (ctx *Context) Made(sentence, name, href, again string) {
	// A folder is its own list, so there is nowhere for the name to lead.
	marked := NoticeName(name)
	if href != "" {
		marked = NoticeLink(name, href)
	}
	ctx.Notify(Notice{
		Kind:    NoticeDone,
		Text:    fmt.Sprintf(sentence, name),
		Markup:  template.HTML(fmt.Sprintf(sentence, marked)),
		Actions: []*NoticeAction{{Label: ctx.T("common.addanother"), Path: again}},
	})
}

// SaveRefusal is the alert for a settings write the store did not keep.
func (ctx *Context) SaveRefusal(err error) string {
	return fmt.Sprintf(ctx.T("form.saverefused"), err)
}
