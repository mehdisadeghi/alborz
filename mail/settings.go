package mail

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2/imapclient"
)

const settingsKey = "base.settings"

const (
	// Named for the warming code, which has no context to read a profile from.
	defaultMessagesPerPage = alborz.DefaultMessagesPerPage
	maxSignature           = 2048
	maxDownloadName        = 80
	maxSignatures          = 20
	maxSignatureName       = 60
	maxFullName            = 512
)

// Signature is one of an account's sign-offs, written under the "-- " delimiter.
type Signature struct {
	Name string
	Text string
}

type Settings struct {
	// Signatures belong to the account, not an identity: the writer picks the persona per message.
	Signatures []Signature
	// DefaultSignature names the one a new message starts with; empty means none.
	DefaultSignature string
	From             string
	// Identities are the other addresses this mailbox may send as, bare or "Name <address>".
	Identities    []string
	Subscriptions []string

	// TrustedAuthServ is the authserv-id whose Authentication-Results verdict is read; empty uses SuggestAuthServ.
	TrustedAuthServ string

	// IndexedSearch is the reader stating the server has a full-text index it does not announce.
	IndexedSearch bool
}

func LoadSettings(ctx context.Context, s alborz.Store) (*Settings, error) {
	settings := &Settings{}
	if err := s.Get(ctx, settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	if key, limit := settings.check(); key != "" {
		return nil, fmt.Errorf("stored settings break %s (%d)", key, limit)
	}
	return settings, nil
}

func (s *Settings) check() (string, int) {
	switch {
	case len(s.Signatures) > maxSignatures:
		return "form.signaturecount", maxSignatures
	case len(s.From) > maxFullName:
		return "form.namelong", maxFullName
	}
	for _, sig := range s.Signatures {
		if len(sig.Text) > maxSignature {
			return "form.signaturelong", maxSignature
		}
		if len(sig.Name) > maxSignatureName {
			return "form.signaturenamelong", maxSignatureName
		}
	}
	return "", 0
}

func (s *Settings) signatureNamed(name string) (Signature, bool) {
	for _, sig := range s.Signatures {
		if sig.Name == name {
			return sig, true
		}
	}
	return Signature{}, false
}

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

func AccountQuery(address string) string {
	if address == "" {
		return ""
	}
	return "?account=" + web.QueryValue(address)
}

func SettingsRail(ctx *web.Context) map[string][]web.RailRow {
	path := ctx.Request().URL.Path
	// Under no account: what this browser keeps.
	rows := map[string][]web.RailRow{"": {
		{Label: ctx.T("settings.general"), Href: ctx.Path("settings.profile"), Active: path == ctx.Path("settings.profile")},
		{Label: ctx.T("passkeys.title"), Href: ctx.Path("settings.passkeys"), Active: strings.HasPrefix(path, ctx.Path("settings.passkeys"))},
	}}
	for _, account := range ctx.Accounts() {
		scoped := ctx.Login != nil && account.Username == ctx.Login.Username()
		q := AccountQuery(account.Username)
		rows[account.Username] = []web.RailRow{
			{Label: ctx.T("settings.account"), Href: ctx.Path("mail.account") + q, Active: scoped && path == ctx.Path("mail.account")},
			{Label: ctx.T("settings.signatures"), Href: ctx.Path("mail.signatures") + q, Active: scoped && strings.HasPrefix(path, ctx.Path("mail.signatures"))},
			{Label: ctx.T("settings.servers"), Href: ctx.Path("settings.servers") + q, Active: scoped && strings.HasPrefix(path, ctx.Path("settings.servers"))},
			{Label: ctx.T("sessions.title"), Href: ctx.Path("settings.sessions") + q, Active: scoped && path == ctx.Path("settings.sessions")},
		}
	}
	return rows
}

func handleSignatures(ctx *web.Context) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	return ctx.Render(http.StatusOK, "settings/signatures", &SignaturesRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.signatures")),
		Settings:       settings,
		Rail:           SettingsRail(ctx),
	})
}

func handleSignatureForm(ctx *web.Context) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	data := &SignatureRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.signatures")),
		Rail:           SettingsRail(ctx),
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
			Rail:           SettingsRail(ctx),
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
	if err := ctx.Login.Store().Put(ctx.Request().Context(), settingsKey, settings); err != nil {
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
	if err := ctx.Login.Store().Put(ctx.Request().Context(), settingsKey, settings); err != nil {
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
	if err := ctx.Login.Store().Put(ctx.Request().Context(), settingsKey, settings); err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.SaveRefusal(err)})
	}
	return ctx.Redirect(http.StatusFound, ctx.URL("mail.signatures"))
}

func (p *plugin) handleSettings(ctx *web.Context) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	var mailboxes []MailboxInfo
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		mailboxes, err = listMailboxes(c)
		return err
	})
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
			Rail:           SettingsRail(ctx),
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
		if err := ctx.Login.Store().Put(ctx.Request().Context(), settingsKey, settings); err != nil {
			return reject(ctx.SaveRefusal(err))
		}
		p.accountChanged(ctx.Login.Username())
		ctx.PutNotice(ctx.T("notice.settingssaved"))
		return ctx.Redirect(http.StatusFound, ctx.URL("mail.account"))
	}

	// The suggestion costs a fetch, so only when nothing is set.
	guess := ""
	if settings.TrustedAuthServ == "" {
		guess = p.SuggestAuthServ(ctx)
	}
	return ctx.Render(http.StatusOK, "settings/account", &SettingsRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.account")),
		AuthServGuess:  guess,
		Settings:       settings,
		Mailboxes:      mailboxes,
		Subscriptions:  Subscriptions(settings.Subscriptions),
		Storage:        storage,
		Rail:           SettingsRail(ctx),
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
	p.accountChanged(ctx.Login.Username())
	ctx.Notify(web.Notice{Kind: web.NoticeDone, Text: ctx.T("settings.forgotten")})
	return ctx.Redirect(http.StatusFound, ctx.URL("mail.account"))
}
