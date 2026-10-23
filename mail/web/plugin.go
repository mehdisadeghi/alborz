package web

import (
	"context"
	"fmt"
	"net/url"
	"regexp"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/mail/imapsearch"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/microcosm-cc/bluemonday"
)

type plugin struct {
	*mail.Mailboxes
	// images match the images alborz holds; composed cleans what the editor wrote, keeping only those.
	images   *regexp.Regexp
	composed *bluemonday.Policy
}

// Mount serves mail.
func Mount(g *web.Group) error {
	s := g.Server()
	p := &plugin{Mailboxes: mail.New(s.Server)}
	p.registerRoutes(g)
	s.Searchers = append(s.Searchers, web.Searcher{Kind: "mail", Answers: imapsearch.Keys(), Rows: "mail-found", Search: p.found, Held: p.held})
	images, err := localImage(s)
	if err != nil {
		return err
	}
	p.images, p.composed = images, mail.ComposedPolicy(images)
	p.NewMail = func(account string, validity uint32, newest imap.UID) {
		devices, err := s.Pushes.Devices(account)
		if err != nil {
			s.Logger().Printf("notification devices: %v", err)
			return
		}
		if len(devices) == 0 {
			return
		}
		item := fmt.Sprintf("mail %s %d %d", account, validity, newest)
		path, _ := s.Path("mail.message.notification.open", "INBOX", newest)
		path = web.QueryPath(path, url.Values{"validity": {fmt.Sprint(validity)}})
		action, _ := s.Path("mail.message.notification", "INBOX", newest)
		action = web.QueryPath(action, url.Values{"validity": {fmt.Sprint(validity)}})
		note := web.PushNote{Key: "push.mail", Item: item, Path: path, Replaces: true, Badge: true,
			Actions: []web.PushAction{{Action: "read", Key: "mailbox.markread", Path: action}, {Action: "delete", Key: "mailbox.delete", Path: action}}}
		for _, login := range s.Logins() {
			if login.Username() != account {
				continue
			}
			preview, err := mail.NotificationPreview(context.Background(), login, newest, validity)
			if err != nil {
				s.Logger().Printf("notification preview: %v", err)
			} else {
				note.Preview = preview
			}
			break
		}
		s.Push(account, note)
	}
	// A restored account skips the login handler, so it is followed here too.
	s.OnAccountReady = append(s.OnAccountReady, p.watchAccount, p.warmAccount)
	s.OnAccountGone = append(s.OnAccountGone, p.ForgetAccount)
	s.Status = append(s.Status, web.Segment{Name: "mail", Show: p.showUnread})
	return nil
}

func (p *plugin) watchAccount(ctx *web.Context, s *alborz.Login) {
	p.Watch(s, ctx.Logger(), ctx.Server.Changes)
}

func (p *plugin) warmAccount(_ *web.Context, s *alborz.Login) {
	p.Warm(s)
}
