package app

import (
	"fmt"
	"log"
	"net/http"

	moonweb "git.mehdix.org/alborz/addons/moon/web"
	motdweb "git.mehdix.org/alborz/addons/motd/web"
	snowweb "git.mehdix.org/alborz/addons/season/web"
	"git.mehdix.org/alborz/addons/weather"
	weatherweb "git.mehdix.org/alborz/addons/weather/web"
	authweb "git.mehdix.org/alborz/auth/web"
	filtersweb "git.mehdix.org/alborz/filters/web"
	mailweb "git.mehdix.org/alborz/mail/web"
	calendarweb "git.mehdix.org/alborz/pim/calendar/web"
	contactsweb "git.mehdix.org/alborz/pim/contacts/web"
	pimweb "git.mehdix.org/alborz/pim/web"
	settingsweb "git.mehdix.org/alborz/settings/web"
	"git.mehdix.org/alborz/web"
)

// Options are the web's, and what the add-ons compiled in take.
type Options struct {
	web.Options
	// MOTD is the directory of message-of-the-day collections; empty offers none.
	MOTD string
	// Weather are where the weather segment asks.
	Weather weather.Sources
}

// Mount composes the groups, from out to in, and mounts each feature on its own.
func Mount(s *web.Server, options Options) error {
	// DAV clients send their password with every request and hold no session.
	dav := s.Group("", "dav")

	page := s.Group("", "", web.Compress, web.SameOrigin, web.Secure, web.Prepare)
	public := page.Group("", "", web.Accounts, web.Noticed)
	// A locked session reaches only the way back in.
	unlock := page.Group("/unlock", "auth", web.Activity, web.SignedIn, web.Noticed)
	app := page.Group("", "", web.Activity, web.Lock, web.SignedIn, web.Noticed)
	// A stream is not compressed, which holds it back, nor the reader acting.
	events := s.Group("", "", web.SameOrigin, web.Secure, web.Prepare, web.Lock, web.SignedIn)

	web.PublicRoutes(public)
	web.EventRoutes(events)
	authweb.Mount(public.Group("", "auth"), unlock, app.Group("", "auth"))
	if err := mailweb.Mount(app.Group("", "mail")); err != nil {
		return err
	}
	settingsweb.Mount(app.Group("", "settings"))
	st, err := pimweb.Mount(dav, app.Group("", "pim"))
	if err != nil {
		return err
	}
	// Calendar before contacts, so the servers page lists the collections on alborz last.
	if err := calendarweb.Mount(app.Group("", "calendar"), st); err != nil {
		return err
	}
	if err := contactsweb.Mount(app.Group("", "contacts"), st); err != nil {
		return err
	}
	filtersweb.Mount(app.Group("/filters", "filters"))
	web.StatusRoutes(app)
	web.SearchRoutes(app)
	// The add-ons compiled in, each under its own name; an operator offers the ones that need a setting.
	snowweb.Mount(app.Group("/addons", "snow"))
	moonweb.Mount(app.Group("", "moon"))
	weatherweb.Mount(app.Group("", "weather"), weather.New(options.Weather, s.Outgoing.Trusted, options.ProjectURL))
	if options.MOTD != "" {
		if err := motdweb.Mount(app.Group("", "motd"), options.MOTD); err != nil {
			return fmt.Errorf("-motd: %w", err)
		}
	}
	web.Fallback(app)
	return nil
}

// New serves alborz with its features mounted.
func New(logger *log.Logger, options Options) (*web.Server, http.Handler, error) {
	s, err := web.New(logger, options.Options, mailweb.TemplateFuncs)
	if err != nil {
		return nil, nil, err
	}
	if err := Mount(s, options); err != nil {
		s.Close()
		return nil, nil, err
	}
	h, err := s.Handler()
	if err != nil {
		s.Close()
		return nil, nil, err
	}
	return s, h, nil
}
