package auth

import "git.mehdix.org/alborz"

// Mount serves signing in and out, locking and unlocking.
func Mount(s *alborz.Server) {
	p := s.Router()
	p.GET("/login", handleLogin)
	p.POST("/login", handleLogin)

	p.POST("/logout", HandleLogout)
	registerPasskeyRoutes(p)
}
