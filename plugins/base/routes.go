package alborzbase

import (
	"net/http"
	"net/url"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/labstack/echo/v4"
)

func registerRoutes(p *alborz.GoPlugin) {
	p.GET("/", func(ctx *alborz.Context) error {
		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	})

	p.GET("/mailbox/:mbox", handleGetMailbox)
	p.GET("/mailbox/:mbox/empty", handleEmptyMailbox)
	p.POST("/mailbox/:mbox/empty", handleEmptyMailbox)
	p.POST("/mailbox/:role/empty-all", handleEmptyAllMailbox)
	p.POST("/mailbox/:mbox", handleGetMailbox)

	p.GET("/new-mailbox", handleNewMailbox)
	p.POST("/new-mailbox", handleNewMailbox)

	p.GET("/delete-mailbox/:mbox", handleDeleteMailbox)
	p.POST("/delete-mailbox/:mbox", handleDeleteMailbox)

	p.GET("/message/:mbox/:uid", func(ctx *alborz.Context) error {
		return handleGetPart(ctx, false)
	})
	p.GET("/message/:mbox/:uid/raw", func(ctx *alborz.Context) error {
		return handleGetPart(ctx, true)
	})
	p.GET("/message/:mbox/:uid/eml", handleDownloadMessage)
	p.POST("/message/:mbox/:uid/invite", handleInvitationReply)
	p.POST("/mailbox/:mbox/refresh", handleRefreshMailbox)
	p.POST("/message/:mbox/export", handleExportMbox)

	p.GET("/login", handleLogin)
	p.POST("/login", handleLogin)
	p.POST("/switch", handleSwitch)

	p.GET("/logout", handleLogout)
	p.POST("/logout", handleLogout)

	p.GET("/compose", handleComposeNew)
	p.POST("/compose", handleComposeNew)

	p.POST("/compose/attachment", handleComposeAttachment)
	p.POST("/compose/attachment/:uuid/remove", handleCancelAttachment)

	p.GET("/message/:mbox/:uid/reply", handleReply)
	p.POST("/message/:mbox/:uid/reply", handleReply)

	p.GET("/message/:mbox/:uid/forward", handleForward)
	p.POST("/message/:mbox/:uid/forward", handleForward)
	p.POST("/message/:mbox/forward", handleForwardSelection)
	p.POST("/message/:mbox/:uid/unsubscribe", handleUnsubscribe)
	p.GET("/message/:mbox/forward", handleForwardAttached)

	p.GET("/message/:mbox/:uid/edit", handleEdit)
	p.POST("/message/:mbox/:uid/edit", handleEdit)

	p.POST("/message/:mbox/move", handleMove)

	p.POST("/message/:mbox/delete", handleDelete)

	p.POST("/message/:mbox/flag", handleSetFlags)

	p.GET("/settings", handleSettings)
	p.POST("/settings", handleSettings)
	p.GET("/signatures", handleSignatures)
	p.POST("/signatures", handleSignatures)
	p.POST("/signatures/delete", handleSignatureDelete)
	p.POST("/signatures/default", handleSignatureDefault)
	p.GET("/settings/browser", handleBrowserSettings)
	p.POST("/settings/browser", handleBrowserSettings)
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
