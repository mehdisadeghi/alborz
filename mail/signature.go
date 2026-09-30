package mail

import (
	"net/http"

	"git.mehdix.org/alborz/web"
)

// handleSignature answers the page's request for its verdict, which may
// stream the whole signed part; the reader leaving drops the connection.
func (p *plugin) handleSignature(ctx *web.Context) error {
	mboxName, uid, err := MessageRef(ctx)
	if err != nil {
		return err
	}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	v, err := p.verify(ctx.Request().Context(), ctx.Login, mboxName, uid, p.TrustedAuthServ(ctx.Login, settings))
	// A verdict that could not be drawn says nothing, as one without a key does.
	if err != nil {
		ctx.Logger().Printf("signature %q uid %v: %v", mboxName, uid, err)
	}
	return ctx.Render(http.StatusOK, "message-signature", &VerdictRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx), Signature: v})
}

// VerdictRenderData fills the page's place for the verdict.
type VerdictRenderData struct {
	web.BaseRenderData
	Signature Verification
}
