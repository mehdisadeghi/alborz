package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
)

type SettingsRenderData struct {
	web.BaseRenderData
	// AuthServGuess is the server's apparent authserv-id, empty when recent mail disagrees.
	AuthServGuess string
	Mailboxes     []mail.MailboxInfo
	Settings      *mail.Settings
	Subscriptions Subscriptions
	// Storage says where this account's settings are written and what is written there.
	Storage SettingsStorage
	Rail    map[string][]web.RailRow
	// Shown is what the form was drawn from, which a save compares the form against.
	Shown string
}

// shownSettings is the part of s the account form shows.
func shownSettings(s *mail.Settings) (string, error) {
	b, err := json.Marshal(mail.Settings{From: s.From, Identities: s.Identities, Subscriptions: s.Subscriptions,
		TrustedAuthServ: s.TrustedAuthServ, IndexedSearch: s.IndexedSearch})
	return string(b), err
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
	Settings *mail.Settings
	Rail     map[string][]web.RailRow
}

// SignatureRenderData is one signature's form, new or existing.
type SignatureRenderData struct {
	web.BaseRenderData
	Editing mail.Signature
	// Was is the name the form started with, so a rename replaces; empty when adding.
	Was  string
	Rail map[string][]web.RailRow
	// Shown is the signature the form was drawn from, which a save compares the form against.
	Shown string
}

func handleSignatures(ctx *web.Context) error {
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
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
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	data := &SignatureRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.signatures")),
		Rail:           web.SettingsRail(ctx),
	}
	if name := ctx.Param("name"); name != "" {
		found, ok := settings.SignatureNamed(name)
		if !ok {
			return alborz.NotFound("notfound.signature", name)
		}
		shown, err := json.Marshal(found)
		if err != nil {
			return err
		}
		data.Editing, data.Was, data.Shown = found, found.Name, string(shown)
	}
	return ctx.Render(http.StatusOK, "settings/signature-edit", data)
}

func handleSignatureSave(ctx *web.Context) error {
	name := strings.TrimSpace(ctx.FormValue("name"))
	text := strings.TrimRight(ctx.FormValue("text"), "\r\n")
	was := ctx.FormValue("was")
	editing := mail.Signature{Name: name, Text: text}
	render := func(message string) error {
		return ctx.Render(http.StatusUnprocessableEntity, "settings/signature-edit", &SignatureRenderData{
			BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.signatures")).Refused(message),
			Editing:        editing,
			Was:            was,
			Rail:           web.SettingsRail(ctx),
			Shown:          ctx.FormValue("shown"),
		})
	}
	switch {
	case name == "":
		return render(ctx.T("form.signaturename"))
	case text == "":
		return render(ctx.T("form.signaturetext"))
	}

	// What the form changed is what it sends that differs from what it showed; the rest stays as stored.
	var drawn *mail.Signature
	if shown := ctx.FormValue("shown"); shown != "" {
		drawn = &mail.Signature{}
		if err := json.Unmarshal([]byte(shown), drawn); err != nil {
			return web.NewError(http.StatusBadRequest, "the form's shown signature does not decode")
		}
	}
	refusal, replaced := "", false
	err := mail.UpdateSettings(ctx.Request().Context(), ctx.Login, func(settings *mail.Settings) error {
		if len(settings.Signatures) >= mail.MaxSignatures && was == "" {
			refusal = fmt.Sprintf(ctx.T("form.signaturecount"), mail.MaxSignatures)
			return errors.New(refusal)
		}
		// A rename keeps the entry's place in the list and its default status.
		for i := range settings.Signatures {
			if settings.Signatures[i].Name != was || was == "" {
				continue
			}
			if drawn != nil {
				stored := &settings.Signatures[i]
				web.KeepChanges(reflect.ValueOf(stored).Elem(), reflect.ValueOf(*drawn), reflect.ValueOf(editing))
				editing = *stored
			}
			if settings.DefaultSignature == was {
				settings.DefaultSignature = editing.Name
			}
			settings.Signatures[i] = editing
			replaced = true
		}
		if !replaced {
			if _, taken := settings.SignatureNamed(name); taken {
				refusal = ctx.T("form.signaturetaken")
				return errors.New(refusal)
			}
			settings.Signatures = append(settings.Signatures, editing)
		}
		if key, limit := settings.Check(); key != "" {
			refusal = fmt.Sprintf(ctx.T(key), limit)
			return errors.New(refusal)
		}
		return nil
	})
	if refusal != "" {
		return render(refusal)
	}
	if err != nil {
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
	name := ctx.FormValue("name")
	err := mail.UpdateSettings(ctx.Request().Context(), ctx.Login, func(settings *mail.Settings) error {
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
		return nil
	})
	if err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.SaveRefusal(err)})
	}
	return ctx.Redirect(http.StatusFound, ctx.URL("mail.signatures"))
}

func handleSignatureDefault(ctx *web.Context) error {
	err := mail.UpdateSettings(ctx.Request().Context(), ctx.Login, func(settings *mail.Settings) error {
		// An empty name clears the default.
		chosen := ctx.FormValue("name")
		if _, ok := settings.SignatureNamed(chosen); !ok {
			chosen = ""
		}
		settings.DefaultSignature = chosen
		return nil
	})
	if err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.SaveRefusal(err)})
	}
	return ctx.Redirect(http.StatusFound, ctx.URL("mail.signatures"))
}

func (p *plugin) handleSettings(ctx *web.Context) error {
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	mailboxes, err := mail.Folders(ctx.Request().Context(), ctx.Login)
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

	if ctx.Request().Method == http.MethodPost {
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		// What the form changed is what it sends that differs from what it showed; the rest stays as stored.
		drawn := *settings
		if v := ctx.FormValue("shown"); v != "" {
			drawn = mail.Settings{}
			if err := json.Unmarshal([]byte(v), &drawn); err != nil {
				return web.NewError(http.StatusBadRequest, "the form's shown settings do not decode")
			}
		}
		mine := drawn
		mine.From = ctx.FormValue("from")
		mine.TrustedAuthServ = strings.TrimSpace(ctx.FormValue("trusted_authserv"))
		mine.IndexedSearch = ctx.FormValue("indexed_search") != ""
		mine.Identities = mail.ParseIdentities(ctx.FormValue("identities"))
		mine.Subscriptions = params["subscriptions"]
		reject := func(message string) error {
			shown, err := shownSettings(&drawn)
			if err != nil {
				return err
			}
			return ctx.Render(http.StatusUnprocessableEntity, "settings/account", &SettingsRenderData{
				BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.account")).Refused(message),
				Settings:       settings,
				Mailboxes:      mailboxes,
				Subscriptions:  Subscriptions(settings.Subscriptions),
				Storage:        storage,
				Rail:           web.SettingsRail(ctx),
				Shown:          shown,
			})
		}
		refusal := ""
		err = mail.UpdateSettings(ctx.Request().Context(), ctx.Login, func(s *mail.Settings) error {
			settings = s
			web.KeepChanges(reflect.ValueOf(s).Elem(), reflect.ValueOf(drawn), reflect.ValueOf(mine))
			if key, limit := settings.Check(); key != "" {
				refusal = fmt.Sprintf(ctx.T(key), limit)
				return errors.New(refusal)
			}
			return nil
		})
		if refusal != "" {
			return reject(refusal)
		}
		if err != nil {
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
	shown, err := shownSettings(settings)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "settings/account", &SettingsRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.account")),
		AuthServGuess:  guess,
		Settings:       settings,
		Mailboxes:      mailboxes,
		Subscriptions:  Subscriptions(settings.Subscriptions),
		Storage:        storage,
		Rail:           web.SettingsRail(ctx),
		Shown:          shown,
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
