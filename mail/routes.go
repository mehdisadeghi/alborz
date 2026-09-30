package mail

import (
	"net/http"

	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
)

// composeBodySize bounds a message form: its text, and files sent with it where no script uploaded them ahead.
const composeBodySize = web.MaxAttachmentSize + web.MaxFormSize

func (p *plugin) registerRoutes(g *web.Group) {
	g.GET("home", "/{$}", func(ctx *web.Context) error {
		return ctx.Redirect(http.StatusFound, ctx.Path("mail.mailbox", "INBOX"))
	})

	g.GET("mailbox", "/mailbox/{mbox}", p.handleGetMailbox)
	g.GET("sidebar", "/mail/sidebar", p.handleSidebar)
	g.GET("search", "/search", p.handleSearch)
	g.GET("mailbox.empty", "/mailbox/{mbox}/empty", p.handleEmptyMailbox)
	g.POST("mailbox.empty", "/mailbox/{mbox}/empty", p.handleEmptyMailbox)
	g.POST("role.empty", "/mailbox/{role}/empty-all", p.handleEmptyAllMailbox)
	g.POST("role.act", "/mailbox/{role}/all/act", p.handleUnifiedAct)
	g.POST("mailbox", "/mailbox/{mbox}", p.handleGetMailbox)

	g.GET("mailbox.create", "/new-mailbox", p.handleNewMailbox)
	g.POST("mailbox.create", "/new-mailbox", p.handleNewMailbox)
	g.GET("import", "/import", p.handleImport)
	g.Stream("import", "/import", importMaxSize, p.handleImport)

	g.GET("mailbox.rename", "/rename-mailbox/{mbox}", p.handleRenameMailbox)
	g.POST("mailbox.rename", "/rename-mailbox/{mbox}", p.handleRenameMailbox)
	g.GET("mailbox.delete", "/delete-mailbox/{mbox}", p.handleDeleteMailbox)
	g.POST("mailbox.delete", "/delete-mailbox/{mbox}", p.handleDeleteMailbox)

	g.GET("message", "/message/{mbox}/{uid}", p.handleGetPart)
	g.GET("message.raw", "/message/{mbox}/{uid}/raw", handleRaw)
	g.GET("message.signature", "/message/{mbox}/{uid}/signature", p.handleSignature)
	g.GET("message.attachments", "/message/{mbox}/{uid}/attachments.zip", handleDownloadAttachments)
	g.GET("message.eml", "/message/{mbox}/{uid}/eml", func(ctx *web.Context) error {
		return handleDownloadMessage(ctx, false)
	})
	g.GET("proxy", "/proxy", handleProxy)
	g.POST("mailbox.refresh", "/mailbox/{mbox}/refresh", p.handleRefreshMailbox)
	g.Stream("mailbox.import", "/mailbox/{mbox}/import", importMaxSize, p.handleDropImport)
	g.GET("messages.export", "/message/{mbox}/export", p.handleExportPage)
	g.POST("messages.export", "/message/{mbox}/export", p.handleExportMbox)

	g.GET("compose", "/compose", p.handleComposeNew)
	g.Upload("compose", "/compose", composeBodySize, p.handleComposeNew, composeTooLarge)

	g.Stream("attachment.add", "/compose/attachment", web.MaxAttachmentSize+web.FormFieldsSize, handleComposeAttachment)
	g.GET("attachment", "/compose/attachment/{uuid}", handleGetAttachment)
	g.POST("attachment.remove", "/compose/attachment/{uuid}/remove", handleCancelAttachment)

	g.GET("message.reply", "/message/{mbox}/{uid}/reply", p.handleReply)
	g.Upload("message.reply", "/message/{mbox}/{uid}/reply", composeBodySize, p.handleReply, composeTooLarge)

	g.GET("message.forward", "/message/{mbox}/{uid}/forward", p.handleForward)
	g.Upload("message.forward", "/message/{mbox}/{uid}/forward", composeBodySize, p.handleForward, composeTooLarge)
	g.POST("messages.forward", "/message/{mbox}/forward", handleForwardSelection)
	g.POST("message.unsubscribe", "/message/{mbox}/{uid}/unsubscribe", handleUnsubscribe)
	g.GET("messages.forward", "/message/{mbox}/forward", p.handleForwardAttached)

	g.GET("message.edit", "/message/{mbox}/{uid}/edit", p.handleEdit)
	g.Upload("message.edit", "/message/{mbox}/{uid}/edit", composeBodySize, p.handleEdit, composeTooLarge)

	g.POST("messages.move", "/message/{mbox}/move", p.handleMove)

	g.POST("messages.delete", "/message/{mbox}/delete", p.handleDelete)

	g.POST("messages.flag", "/message/{mbox}/flag", p.handleSetFlags)

	g.GET("signatures", "/signatures", web.Scoped(handleSignatures))
	g.GET("signatures.create", "/signatures/create", web.Scoped(handleSignatureForm))
	g.GET("signature", "/signatures/{name}", web.Scoped(handleSignatureForm))
	g.POST("signatures", "/signatures", handleSignatureSave)
	g.POST("signatures.delete", "/signatures/delete", handleSignatureDelete)
	g.POST("signatures.default", "/signatures/default", handleSignatureDefault)
	g.GET("account", "/settings/account", web.Scoped(p.handleSettings))
	g.POST("account", "/settings/account", p.handleSettings)
	g.POST("account.forget", "/settings/account/forget", p.handleForget)
}

// folderPath is a folder's address, for code that builds many.
func folderPath(ctx *web.Context) func(string) string {
	return func(name string) string { return ctx.Path("mail.mailbox", name) }
}

func mailboxURL(ctx *web.Context, mboxName string) string {
	return ctx.URL("mail.mailbox", mboxName)
}

func mailboxRef(ctx *web.Context) (string, error) {
	return ctx.Param("mbox"), nil
}

// MessageRef is the message the route's path names.
func MessageRef(ctx *web.Context) (string, imap.UID, error) {
	uid, err := ParseUID(ctx.Param("uid"))
	if err != nil {
		return "", 0, web.NewError(http.StatusBadRequest, err)
	}
	return ctx.Param("mbox"), uid, nil
}
