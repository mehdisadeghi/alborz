package web

import "git.mehdix.org/alborz/web"

// Mount serves signing in on public, unlocking on unlock, which a locked session reaches, and signing
// out and locking on app.
func Mount(public, unlock, app *web.Group) {
	public.GET("login", "/login", handleLogin)
	public.POST("login", "/login", handleLogin)

	app.POST("logout", "/logout", HandleLogout)
	registerPasskeyRoutes(unlock, app)
}
