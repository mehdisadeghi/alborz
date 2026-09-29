package settings

import (
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
)

// Mount serves the settings pages.
func Mount(s *web.Server) {
	p := s.Router()
	p.GET("/settings", handleProfile)
	p.POST("/settings", handleProfile)
	p.POST("/settings/profile/take", handleProfileTake)
	p.POST("/settings/profile/discard", handleProfileDiscard)
	p.GET("/settings/sessions", mail.Scoped(handleSessions))
	p.POST("/settings/sessions", mail.Scoped(handleSessions))
	p.GET("/settings/servers", mail.Scoped(handleServers))
	p.GET("/settings/servers/:server", mail.Scoped(handleServer))
	p.POST("/settings/servers/:server", mail.Scoped(handleServer))
	p.POST("/language", handleLanguage)
	registerPasskeyRoutes(p)
}
