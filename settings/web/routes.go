package web

import (
	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

// Mount serves the settings pages.
func Mount(g *web.Group) {
	scoped := g.Group("", "", web.Scoped)
	g.Page("profile", "/settings", handleProfile)
	g.POST("profile.take", "/settings/profile/take", handleProfileTake)
	g.POST("profile.discard", "/settings/profile/discard", handleProfileDiscard)
	scoped.Page("sessions", "/settings/sessions", handleSessions)
	scoped.Page("password", "/settings/password", handlePassword)
	scoped.GET("data", "/settings/data", handleData)
	scoped.Stream("data", "/settings/data", alborz.ArchiveMaxSize+web.FormFieldsSize, handleImport)
	scoped.Verbatim("data.export", "/settings/data/export", handleExport)
	scoped.Page("encryption", "/settings/encryption", handleEncryption)
	scoped.POST("encryption.off", "/settings/encryption/off", handleDecrypt)
	scoped.POST("encryption.recover", "/settings/encryption/recover", handleRecover)
	scoped.POST("encryption.startover", "/settings/encryption/start-over", handleStartOver)
	scoped.GET("notifications", "/settings/notifications", handleNotifications)
	scoped.Page("notifications.create", "/settings/notifications/create", handleNotificationCreate)
	scoped.POST("notifications.off", "/settings/notifications/off", handleNotificationOff)
	scoped.GET("servers", "/settings/servers", handleServers)
	scoped.Page("server", "/settings/servers/{server}", handleServer)
	g.POST("language", "/language", handleLanguage)
	registerPasskeyRoutes(g)
	srv := g.Server()
	srv.OnServers = append(srv.OnServers, mailCards)
	srv.OnAccountReady = append(srv.OnAccountReady, warnLocked)
}
