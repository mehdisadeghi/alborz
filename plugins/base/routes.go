package alborzbase

import (
	"net/http"
	"net/url"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/labstack/echo/v4"
)

func (p *plugin) registerRoutes() {
	p.GET("/", func(ctx *alborz.Context) error {
		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	})

	p.GET("/mailbox/:mbox", p.handleGetMailbox)
	p.GET("/mail/sidebar", p.handleSidebar)
	p.GET("/search", p.handleSearch)
	p.GET("/mailbox/:mbox/empty", p.handleEmptyMailbox)
	p.POST("/mailbox/:mbox/empty", p.handleEmptyMailbox)
	p.POST("/mailbox/:role/empty-all", p.handleEmptyAllMailbox)
	p.POST("/mailbox/:role/all/act", p.handleUnifiedAct)
	p.POST("/mailbox/:mbox", p.handleGetMailbox)

	p.GET("/new-mailbox", p.handleNewMailbox)
	p.POST("/new-mailbox", p.handleNewMailbox)
	p.GET("/import", p.handleImport)
	p.POST("/import", p.handleImport)

	p.GET("/rename-mailbox/:mbox", p.handleRenameMailbox)
	p.POST("/rename-mailbox/:mbox", p.handleRenameMailbox)
	p.GET("/delete-mailbox/:mbox", p.handleDeleteMailbox)
	p.POST("/delete-mailbox/:mbox", p.handleDeleteMailbox)

	p.GET("/message/:mbox/:uid", func(ctx *alborz.Context) error {
		return p.handleGetPart(ctx, false)
	})
	p.GET("/message/:mbox/:uid/raw", func(ctx *alborz.Context) error {
		return p.handleGetPart(ctx, true)
	})
	p.GET("/message/:mbox/:uid/attachments.zip", handleDownloadAttachments)
	p.GET("/message/:mbox/:uid/eml", func(ctx *alborz.Context) error {
		return handleDownloadMessage(ctx, false)
	})
	p.POST("/message/:mbox/:uid/invite", handleInvitationReply)
	p.POST("/mailbox/:mbox/refresh", p.handleRefreshMailbox)
	p.POST("/mailbox/:mbox/import", p.handleDropImport)
	p.GET("/message/:mbox/export", p.handleExportPage)
	p.POST("/message/:mbox/export", p.handleExportMbox)

	p.GET("/login", handleLogin)
	p.POST("/login", handleLogin)

	p.POST("/logout", handleLogout)
	registerPasskeyRoutes(&p.GoPlugin)

	p.GET("/compose", p.handleComposeNew)
	p.POST("/compose", p.handleComposeNew)

	p.POST("/compose/attachment", handleComposeAttachment)
	p.GET("/compose/attachment/:uuid", handleGetAttachment)
	p.POST("/compose/attachment/:uuid/remove", handleCancelAttachment)

	p.GET("/message/:mbox/:uid/reply", p.handleReply)
	p.POST("/message/:mbox/:uid/reply", p.handleReply)

	p.GET("/message/:mbox/:uid/forward", p.handleForward)
	p.POST("/message/:mbox/:uid/forward", p.handleForward)
	p.POST("/message/:mbox/forward", handleForwardSelection)
	p.POST("/message/:mbox/:uid/unsubscribe", handleUnsubscribe)
	p.GET("/message/:mbox/forward", p.handleForwardAttached)

	p.GET("/message/:mbox/:uid/edit", p.handleEdit)
	p.POST("/message/:mbox/:uid/edit", p.handleEdit)

	p.POST("/message/:mbox/move", p.handleMove)

	p.POST("/message/:mbox/delete", p.handleDelete)

	p.POST("/message/:mbox/flag", p.handleSetFlags)

	// Settings are one account's, so a bare URL with several signed in
	// names the account it opened on, and the page is where it says.
	scoped := func(h func(*alborz.Context) error) func(*alborz.Context) error {
		return func(ctx *alborz.Context) error {
			if ctx.Unified && ctx.Request().Method == http.MethodGet {
				return ctx.Redirect(http.StatusFound, ctx.Request().URL.Path+accountQuery(ctx.Login.Username()))
			}
			return h(ctx)
		}
	}
	// Settings that belong to the reader need no account; an account's
	// own do, and the scoped wrapper puts it in the URL rather than
	// leaving the page to guess.
	p.GET("/settings", handleProfile)
	p.POST("/settings", handleProfile)
	p.POST("/settings/profile/take", handleProfileTake)
	p.POST("/settings/profile/discard", handleProfileDiscard)
	p.GET("/signatures", scoped(handleSignatures))
	p.GET("/signatures/create", scoped(handleSignatureForm))
	p.GET("/signatures/:name", scoped(handleSignatureForm))
	p.POST("/signatures", handleSignatureSave)
	p.POST("/signatures/delete", handleSignatureDelete)
	p.POST("/signatures/default", handleSignatureDefault)
	p.GET("/settings/sessions", scoped(handleSessions))
	p.POST("/settings/sessions", scoped(handleSessions))
	p.GET("/settings/servers", scoped(handleServers))
	p.GET("/settings/servers/:server", scoped(handleServer))
	p.POST("/settings/servers/:server", scoped(handleServer))
	p.GET("/settings/account", scoped(p.handleSettings))
	p.POST("/settings/account", p.handleSettings)
	p.POST("/settings/account/forget", p.handleForget)
	p.POST("/language", handleLanguage)
}

// mailboxURL is the page of a folder, under the account the request
// named.
func mailboxURL(ctx *alborz.Context, mboxName string) string {
	return ctx.AccountPath("/mailbox/" + url.PathEscape(mboxName))
}

// mailboxRef reads the folder a route names. A name that does not
// unescape is a malformed link, which is the caller's fault.
func mailboxRef(ctx *alborz.Context) (string, error) {
	name, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return "", echo.NewHTTPError(http.StatusBadRequest, err)
	}
	return name, nil
}

// messageRef reads the folder and UID a route names, with the same
// answer for a malformed one.
func messageRef(ctx *alborz.Context) (string, imap.UID, error) {
	mboxName, uid, err := parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return "", 0, echo.NewHTTPError(http.StatusBadRequest, err)
	}
	return mboxName, uid, nil
}
