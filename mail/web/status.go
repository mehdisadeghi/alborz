package web

import (
	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

// showUnread counts the unread mail in the inbox of the request's account, or of every one merged.
func (p *plugin) showUnread(ctx *web.Context) (web.Shown, error) {
	logins := []*alborz.Login{ctx.Login}
	if ctx.Unified {
		logins = ctx.Logins()
	}
	unread := 0
	for _, login := range logins {
		rail, err := p.Rail(login)
		if err != nil {
			return web.Shown{}, err
		}
		if in := rail.Inbox; in != nil && in.StatusData != nil && in.NumUnseen != nil {
			unread += int(*in.NumUnseen)
		}
	}
	return web.Shown{
		Parts: []string{ctx.Tf("mail.status.unread", unread)},
		Link:  ctx.AccountPath(ctx.Path("mail.mailbox", "INBOX") + "?view=" + alborz.ViewUnread),
	}, nil
}
