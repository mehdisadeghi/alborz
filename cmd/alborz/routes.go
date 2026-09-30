package main

import (
	authweb "git.mehdix.org/alborz/auth/web"
	filtersweb "git.mehdix.org/alborz/filters/web"
	mailweb "git.mehdix.org/alborz/mail/web"
	calendarweb "git.mehdix.org/alborz/pim/calendar/web"
	contactsweb "git.mehdix.org/alborz/pim/contacts/web"
	pimweb "git.mehdix.org/alborz/pim/web"
	settingsweb "git.mehdix.org/alborz/settings/web"
	"git.mehdix.org/alborz/web"
)

// mount composes the groups, from out to in, and mounts each feature on its own.
func mount(s *web.Server) error {
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
	web.Fallback(app)
	return nil
}
