package web

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

// EncryptionRenderData renders encryption.html.
type EncryptionRenderData struct {
	web.BaseRenderData
	Rail map[string][]web.RailRow
	alborz.Encryption
	// Recovery is the recovery key, on the one page that shows it; RecoveryFile is it as a download.
	Recovery     string
	RecoveryFile template.URL
}

// renderEncryption shows recovery, a new recovery key, under the notice said.
func renderEncryption(ctx *web.Context, status int, alert, recovery, said string) error {
	state, err := ctx.Server.Encryption(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	data := &EncryptionRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.encryption")).Refused(alert),
		Rail:           web.SettingsRail(ctx),
		Encryption:     state,
		Recovery:       recovery,
	}
	if recovery != "" {
		data.GlobalData.Notice = &web.Notice{Kind: web.NoticeDone, Text: ctx.T(said)}
		text := fmt.Sprintf(ctx.T("encryption.recoveryfile"), ctx.Login.Username(), recovery) + "\n"
		data.RecoveryFile = template.URL("data:text/plain;charset=utf-8," + url.PathEscape(text))
	}
	return ctx.Render(status, "settings/encryption", data)
}

func handleEncryption(ctx *web.Context) error {
	if ctx.Request().Method != http.MethodPost {
		return renderEncryption(ctx, http.StatusOK, "", "", "")
	}
	recovery, err := ctx.Server.Encrypt(ctx.Request().Context(), ctx.Login, ctx.FormValue("password"), ctx.RealIP())
	if err != nil {
		return encryptionRefusal(ctx, err)
	}
	return renderEncryption(ctx, http.StatusOK, "", recovery, "notice.encryptionon")
}

func handleDecrypt(ctx *web.Context) error {
	err := ctx.Server.Decrypt(ctx.Request().Context(), ctx.Login, ctx.FormValue("password"), ctx.RealIP())
	if err != nil {
		return encryptionRefusal(ctx, err)
	}
	ctx.PutNotice(ctx.T("notice.encryptionoff"))
	return ctx.Redirect(http.StatusFound, ctx.URL("settings.encryption"))
}

func handleRecover(ctx *web.Context) error {
	if err := ctx.Server.Recover(ctx.Request().Context(), ctx.Login, ctx.FormValue("recovery")); err != nil {
		return encryptionRefusal(ctx, err)
	}
	ctx.PutNotice(ctx.T("notice.encryptionopened"))
	return ctx.Redirect(http.StatusFound, ctx.URL("settings.encryption"))
}

func handleStartOver(ctx *web.Context) error {
	if ctx.FormValue("confirm") != "1" {
		return renderEncryption(ctx, http.StatusUnprocessableEntity, ctx.T("encryption.confirmneeded"), "", "")
	}
	recovery, err := ctx.Server.StartOver(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	if recovery != "" {
		return renderEncryption(ctx, http.StatusOK, "", recovery, "notice.startedover")
	}
	ctx.PutNotice(ctx.T("notice.startedover"))
	return ctx.Redirect(http.StatusFound, ctx.URL("settings.encryption"))
}

func encryptionRefusal(ctx *web.Context, err error) error {
	var paused alborz.PausedError
	switch {
	case errors.As(err, &paused):
		text, status := ctx.SignInRefusal(err)
		return renderEncryption(ctx, status, text, "", "")
	case errors.As(err, new(alborz.AuthError)):
		return renderEncryption(ctx, http.StatusUnprocessableEntity, ctx.T("encryption.wrongpassword"), "", "")
	case errors.Is(err, alborz.ErrRecoveryKey):
		return renderEncryption(ctx, http.StatusUnprocessableEntity, ctx.T("encryption.wrongrecovery"), "", "")
	case errors.Is(err, errors.ErrUnsupported), errors.Is(err, alborz.ErrLocked):
		return renderEncryption(ctx, http.StatusUnprocessableEntity, ctx.SaveRefusal(err), "", "")
	}
	return err
}

// warnLocked tells, at sign-in, that the account's data does not open with the password it signed in with.
func warnLocked(ctx *web.Context, l *alborz.Login) {
	if !l.Locked() {
		return
	}
	ctx.Notify(web.Notice{
		Kind: web.NoticeWarning,
		Text: fmt.Sprintf(ctx.T("notice.encryptionlocked"), l.Username()),
		Actions: []*web.NoticeAction{{Label: ctx.T("settings.encryption"),
			Path: ctx.Path("settings.encryption") + web.AccountQuery(l.Username())}},
	})
}
