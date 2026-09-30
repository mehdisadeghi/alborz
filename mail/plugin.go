package mail

import (
	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
	"github.com/microcosm-cc/bluemonday"
)

type plugin struct {
	*Mailboxes
	// composed cleans what the editor wrote.
	composed *bluemonday.Policy
}

// Mount serves mail.
func Mount(g *web.Group) error {
	s := g.Server()
	p := &plugin{Mailboxes: New(s.Server)}
	p.registerRoutes(g)
	images, err := localImage(s)
	if err != nil {
		return err
	}
	p.composed = ComposedPolicy(images)
	// A restored account skips the login handler, so it is followed here too.
	s.OnAccountReady = append(s.OnAccountReady, p.watchAccount, p.warmAccount)
	s.OnAccountGone = append(s.OnAccountGone, p.ForgetAccount)
	return nil
}

func (p *plugin) watchAccount(ctx *web.Context, s *alborz.Login) {
	p.Watch(s, ctx.Logger(), ctx.Server.Changes)
}

func (p *plugin) warmAccount(_ *web.Context, s *alborz.Login) {
	p.Warm(s)
}
