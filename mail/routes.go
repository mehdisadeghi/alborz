package mail

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
	p.GET("/proxy", handleProxy)
	p.POST("/mailbox/:mbox/refresh", p.handleRefreshMailbox)
	p.POST("/mailbox/:mbox/import", p.handleDropImport)
	p.GET("/message/:mbox/export", p.handleExportPage)
	p.POST("/message/:mbox/export", p.handleExportMbox)

	p.GET("/login", handleLogin)
	p.POST("/login", handleLogin)

	p.POST("/logout", HandleLogout)
	registerPasskeyRoutes(p.Router)

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

	p.GET("/signatures", Scoped(handleSignatures))
	p.GET("/signatures/create", Scoped(handleSignatureForm))
	p.GET("/signatures/:name", Scoped(handleSignatureForm))
	p.POST("/signatures", handleSignatureSave)
	p.POST("/signatures/delete", handleSignatureDelete)
	p.POST("/signatures/default", handleSignatureDefault)
	p.GET("/settings/account", Scoped(p.handleSettings))
	p.POST("/settings/account", p.handleSettings)
	p.POST("/settings/account/forget", p.handleForget)
}

// Scoped serves h per account: settings are per account, so a unified GET redirects to name its account.
func Scoped(h alborz.HandlerFunc) alborz.HandlerFunc {
	return func(ctx *alborz.Context) error {
		if ctx.Unified && ctx.Request().Method == http.MethodGet {
			return ctx.Redirect(http.StatusFound, ctx.Request().URL.Path+AccountQuery(ctx.Login.Username()))
		}
		return h(ctx)
	}
}

func mailboxURL(ctx *alborz.Context, mboxName string) string {
	return ctx.AccountPath("/mailbox/" + url.PathEscape(mboxName))
}

func mailboxRef(ctx *alborz.Context) (string, error) {
	name, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return "", echo.NewHTTPError(http.StatusBadRequest, err)
	}
	return name, nil
}

func messageRef(ctx *alborz.Context) (string, imap.UID, error) {
	mboxName, uid, err := parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return "", 0, echo.NewHTTPError(http.StatusBadRequest, err)
	}
	return mboxName, uid, nil
}
