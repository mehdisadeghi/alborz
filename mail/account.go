package mail

import (
	"fmt"
	"net/http"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

type SettingsRenderData struct {
	web.BaseRenderData
	// AuthServGuess is the server's apparent authserv-id, empty when recent mail disagrees.
	AuthServGuess string
	Mailboxes     []MailboxInfo
	Settings      *Settings
	Subscriptions Subscriptions
	// Storage says where this account's settings are written and what is written there.
	Storage SettingsStorage
	Rail    map[string][]web.RailRow
}

// SettingsStorage says where an account's settings live, so the reader can take them back.
type SettingsStorage struct {
	OnServer bool
	Host     string
	Entries  []string
}

func settingsStorage(ctx *web.Context) (SettingsStorage, error) {
	store, ok := ctx.Login.Store().(alborz.Settings)
	if !ok {
		return SettingsStorage{}, nil
	}
	entries, err := store.Entries(ctx.Request().Context())
	if err != nil {
		return SettingsStorage{}, err
	}
	_, host, _ := strings.Cut(ctx.Origin(), "://")
	if store.OnServer() {
		_, domain, _ := strings.Cut(ctx.Login.Username(), "@")
		host = ctx.Server.UpstreamsFor(domain).IMAP
	}
	return SettingsStorage{OnServer: store.OnServer(), Host: host, Entries: entries}, nil
}

type Subscriptions []string

func (s Subscriptions) Has(sub string) bool {
	for _, cand := range s {
		if cand == sub {
			return true
		}
	}
	return false
}

// SignaturesRenderData is the signature list.
type SignaturesRenderData struct {
	web.BaseRenderData
	Settings *Settings
	Rail     map[string][]web.RailRow
}

// SignatureRenderData is one signature's form, new or existing.
type SignatureRenderData struct {
	web.BaseRenderData
	Editing Signature
	// Was is the name the form started with, so a rename replaces; empty when adding.
	Was  string
	Rail map[string][]web.RailRow
}

func handleSignatures(ctx *web.Context) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	return ctx.Render(http.StatusOK, "settings/signatures", &SignaturesRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.signatures")),
		Settings:       settings,
		Rail:           web.SettingsRail(ctx),
	})
}

func handleSignatureForm(ctx *web.Context) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	data := &SignatureRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.signatures")),
		Rail:           web.SettingsRail(ctx),
	}
	if name := ctx.Param("name"); name != "" {
		found, ok := settings.signatureNamed(name)
		if !ok {
			return alborz.NotFound("notfound.signature", name)
		}
		data.Editing, data.Was = found, found.Name
	}
	return ctx.Render(http.StatusOK, "settings/signature-edit", data)
}

func handleSignatureSave(ctx *web.Context) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	name := strings.TrimSpace(ctx.FormValue("name"))
	text := strings.TrimRight(ctx.FormValue("text"), "\r\n")
	was := ctx.FormValue("was")
	editing := Signature{Name: name, Text: text}
	render := func(message string) error {
		return ctx.Render(http.StatusUnprocessableEntity, "settings/signature-edit", &SignatureRenderData{
			BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.signatures")).Refused(message),
			Editing:        editing,
			Was:            was,
			Rail:           web.SettingsRail(ctx),
		})
	}
	switch {
	case name == "":
		return render(ctx.T("form.signaturename"))
	case text == "":
		return render(ctx.T("form.signaturetext"))
	case len(settings.Signatures) >= maxSignatures && was == "":
		return render(fmt.Sprintf(ctx.T("form.signaturecount"), maxSignatures))
	}

	// A rename keeps the entry's place in the list and its default status.
	replaced := false
	for i := range settings.Signatures {
		if settings.Signatures[i].Name != was || was == "" {
			continue
		}
		if settings.DefaultSignature == was {
			settings.DefaultSignature = name
		}
		settings.Signatures[i] = editing
		replaced = true
	}
	if !replaced {
		if _, taken := settings.signatureNamed(name); taken {
			return render(ctx.T("form.signaturetaken"))
		}
		settings.Signatures = append(settings.Signatures, editing)
	}
	if key, limit := settings.check(); key != "" {
		return render(fmt.Sprintf(ctx.T(key), limit))
	}
	if err := SaveSettings(ctx.Request().Context(), ctx.Login.Store(), settings); err != nil {
		return render(ctx.SaveRefusal(err))
	}
	if !replaced {
		ctx.Made(ctx.T("notice.signaturecreated"), name,
			ctx.URL("mail.signature", name),
			ctx.URL("mail.signatures.create"))
	}
	return ctx.Redirect(http.StatusFound, ctx.URL("mail.signatures"))
}

func handleSignatureDelete(ctx *web.Context) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	name := ctx.FormValue("name")
	kept := settings.Signatures[:0]
	for _, sig := range settings.Signatures {
		if sig.Name != name {
			kept = append(kept, sig)
		}
	}
	settings.Signatures = kept
	if settings.DefaultSignature == name {
		settings.DefaultSignature = ""
	}
	if err := SaveSettings(ctx.Request().Context(), ctx.Login.Store(), settings); err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.SaveRefusal(err)})
	}
	return ctx.Redirect(http.StatusFound, ctx.URL("mail.signatures"))
}

func handleSignatureDefault(ctx *web.Context) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	// An empty name clears the default.
	chosen := ctx.FormValue("name")
	if _, ok := settings.signatureNamed(chosen); !ok {
		chosen = ""
	}
	settings.DefaultSignature = chosen
	if err := SaveSettings(ctx.Request().Context(), ctx.Login.Store(), settings); err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.SaveRefusal(err)})
	}
	return ctx.Redirect(http.StatusFound, ctx.URL("mail.signatures"))
}

func (p *plugin) handleSettings(ctx *web.Context) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	mailboxes, err := Folders(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	// A server that will not answer for storage says so on the page; it does not refuse the settings.
	storage, err := settingsStorage(ctx)
	if err != nil {
		ctx.Logger().Printf("settings: listing what %s keeps: %v", ctx.Login.Username(), err)
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("notice.noanswer")})
		storage = SettingsStorage{}
	}

	reject := func(message string) error {
		return ctx.Render(http.StatusUnprocessableEntity, "settings/account", &SettingsRenderData{
			BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.account")).Refused(message),
			Settings:       settings,
			Mailboxes:      mailboxes,
			Subscriptions:  Subscriptions(settings.Subscriptions),
			Storage:        storage,
			Rail:           web.SettingsRail(ctx),
		})
	}

	if ctx.Request().Method == http.MethodPost {
		settings.From = ctx.FormValue("from")
		settings.TrustedAuthServ = strings.TrimSpace(ctx.FormValue("trusted_authserv"))
		settings.IndexedSearch = ctx.FormValue("indexed_search") != ""
		settings.Identities = parseIdentities(ctx.FormValue("identities"))

		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		settings.Subscriptions = params["subscriptions"]

		if key, limit := settings.check(); key != "" {
			return reject(fmt.Sprintf(ctx.T(key), limit))
		}
		if err := SaveSettings(ctx.Request().Context(), ctx.Login.Store(), settings); err != nil {
			return reject(ctx.SaveRefusal(err))
		}
		p.Changed(ctx.Login.Username())
		ctx.PutNotice(ctx.T("notice.settingssaved"))
		return ctx.Redirect(http.StatusFound, ctx.URL("mail.account"))
	}

	// The suggestion costs a fetch, so only when nothing is set.
	guess := ""
	if settings.TrustedAuthServ == "" {
		guess = p.SuggestAuthServ(ctx.Login)
	}
	return ctx.Render(http.StatusOK, "settings/account", &SettingsRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.account")),
		AuthServGuess:  guess,
		Settings:       settings,
		Mailboxes:      mailboxes,
		Subscriptions:  Subscriptions(settings.Subscriptions),
		Storage:        storage,
		Rail:           web.SettingsRail(ctx),
	})
}

func (p *plugin) handleForget(ctx *web.Context) error {
	store, ok := ctx.Login.Store().(alborz.Settings)
	if !ok {
		// Nothing else outlives the process to forget.
		ctx.Notify(web.Notice{Kind: web.NoticeDone, Text: ctx.T("settings.forgotten")})
		return ctx.Redirect(http.StatusFound, ctx.URL("mail.account"))
	}
	if err := store.Forget(ctx.Request().Context()); err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.SaveRefusal(err)})
		return ctx.Redirect(http.StatusFound, ctx.URL("mail.account"))
	}
	p.Changed(ctx.Login.Username())
	ctx.Notify(web.Notice{Kind: web.NoticeDone, Text: ctx.T("settings.forgotten")})
	return ctx.Redirect(http.StatusFound, ctx.URL("mail.account"))
}
