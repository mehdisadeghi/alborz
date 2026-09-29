package web

import (
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
)

// Mount serves the settings pages.
func Mount(g *web.Group) {
	g.GET("profile", "/settings", handleProfile)
	g.POST("profile", "/settings", handleProfile)
	g.POST("profile.take", "/settings/profile/take", handleProfileTake)
	g.POST("profile.discard", "/settings/profile/discard", handleProfileDiscard)
	g.GET("sessions", "/settings/sessions", mail.Scoped(handleSessions))
	g.POST("sessions", "/settings/sessions", mail.Scoped(handleSessions))
	g.GET("servers", "/settings/servers", mail.Scoped(handleServers))
	g.GET("server", "/settings/servers/{server}", mail.Scoped(handleServer))
	g.POST("server", "/settings/servers/{server}", mail.Scoped(handleServer))
	g.POST("language", "/language", handleLanguage)
	registerPasskeyRoutes(g)
	srv := g.Server()
	srv.OnServers = append(srv.OnServers, mailCards)
}
