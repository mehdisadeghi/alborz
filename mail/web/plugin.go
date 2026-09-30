package web

import (
	"fmt"
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
	inbox, err := s.Path("mail.mailbox", "INBOX")
	if err != nil {
		return err
	}
	p.NewMail = func(account string, validity uint32, newest imap.UID) {
		item := fmt.Sprintf("mail %s %d %d", account, validity, newest)
		s.Push(account, web.PushNote{Key: "push.mail", Item: item, Path: inbox, Replaces: true, Badge: true})
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
