package alborzbase

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/labstack/echo/v4"
)

const settingsKey = "base.settings"

func init() {
	alborz.RegisterSetting(settingsKey)
}

const (
	maxMessagesPerPage = 100
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

var serviceRefusals = map[error]string{
	alborz.ErrServiceNotURL:   "settings.servernoturl",
	alborz.ErrServiceNotHTTPS: "settings.servernothttps",
	alborz.ErrServiceNoHost:   "settings.servernohost",
	alborz.ErrServicePrivate:  "settings.serverprivate",
}

type SettingsRenderData struct {
	alborz.BaseRenderData
	// AuthServGuess is the server's apparent authserv-id, empty when recent mail disagrees.
	AuthServGuess string
	Mailboxes     []MailboxInfo
	Settings      *Settings
	Subscriptions Subscriptions
	// Storage says where this account's settings are written and what is written there.
	Storage SettingsStorage
	Rail    map[string][]alborz.RailRow
}

// SettingsStorage says where an account's settings live, so the reader can take them back.
type SettingsStorage struct {
	OnServer bool
	Host     string
	Entries  []string
}

func settingsStorage(ctx *alborz.Context) (SettingsStorage, error) {
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

// ServerInfo is what alborz can say about the account's upstreams.
type ServerInfo struct {
	IMAP  string
	SMTP  string
	Sieve string
	// Agent is what the IMAP server calls itself (RFC 2971 ID); empty without the extension.
	Agent string
	// Abilities are the capabilities that change what alborz does.
	Abilities []Ability
	// Explained is the same list as prose.
	Explained []alborz.Explained
}

// Ability is one capability named for what it means to the reader.
type Ability struct {
	Label string
	Hint  string
	Have  bool
	// Stated marks an ability the reader claimed in settings rather than the server announced.
	Stated bool
}

func abilities(c *imapclient.Client, settings *Settings) []Ability {
	caps := c.Caps()
	indexed := caps.Has(imap.CapSearchFuzzy)
	return []Ability{
		{"settings.abilitysort", "settings.abilitysorthint", caps.Has(imap.CapSort), false},
		{"settings.abilitythread", "settings.abilitythreadhint", caps.Has(imap.Cap("THREAD=REFERENCES")), false},
		{"settings.abilitysettings", "settings.abilitysettingshint", caps.Has(imap.CapMetadata), false},
		{"settings.abilityquota", "settings.abilityquotahint", caps.Has(imap.CapQuota), false},
		{"settings.abilitypush", "settings.abilitypushhint", caps.Has(imap.CapIdle), false},
		{"settings.abilitycounts", "settings.abilitycountshint", caps.Has(imap.CapListStatus), false},
		{"settings.abilityindex", "settings.abilityindexhint", indexed || settings.IndexedSearch, !indexed && settings.IndexedSearch},
		{"settings.abilityuidplus", "settings.abilityuidplushint", caps.Has(imap.CapUIDPlus), false},
		{"settings.abilitysize", "settings.abilitysizehint", caps.Has(imap.CapStatusSize), false},
		{"settings.abilityid", "settings.abilityidhint", caps.Has(imap.CapID), false},
	}
}

// ProfileRenderData is the settings page that belongs to no account.
type ProfileRenderData struct {
	alborz.BaseRenderData
	Rail  map[string][]alborz.RailRow
	Theme string
	// Themes are the overlays on offer, built in or dropped in by a deployment.
	Themes        []alborz.Theme
	AlignByScript bool
	TextSize      string

	Reading    alborz.Reading
	Writing    alborz.Writing
	MaxPerPage int
	// LockMinutes is how long this browser waits before it locks; zero without a passkey.
	LockMinutes int
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

func serverAgent(c *imapclient.Client) string {
	if !c.Caps().Has(imap.CapID) {
		return ""
	}
	data, err := c.ID(&imap.IDData{Name: alborz.BrandName}).Wait()
	if err != nil || data == nil || data.Name == "" {
		return ""
	}
	if data.Version == "" {
		return data.Name
	}
	return data.Name + " " + data.Version
}

func serverInfo(ctx *alborz.Context, agent string, abilities []Ability) ServerInfo {
	_, domain, _ := strings.Cut(ctx.Login.Username(), "@")
	up := ctx.Server.UpstreamsFor(domain)
	explained := make([]alborz.Explained, 0, len(abilities))
	for _, a := range abilities {
		explained = append(explained, alborz.Explained{
			Term: ctx.T(a.Label), Hint: ctx.T(a.Hint)})
	}
	return ServerInfo{IMAP: up.IMAP, SMTP: up.SMTP, Sieve: up.Sieve,
		Agent: agent, Abilities: abilities, Explained: explained}
}

// SignaturesRenderData is the signature list.
type SignaturesRenderData struct {
	alborz.BaseRenderData
	Settings *Settings
	Rail     map[string][]alborz.RailRow
}

// SignatureRenderData is one signature's form, new or existing.
type SignatureRenderData struct {
	alborz.BaseRenderData
	Editing Signature
	// Was is the name the form started with, so a rename replaces; empty when adding.
	Was  string
	Rail map[string][]alborz.RailRow
}

// ServersRenderData is where the account's mail lives and what the software there can do.
type ServersRenderData struct {
	alborz.BaseRenderData
	Rail map[string][]alborz.RailRow
	// Showing is the server the page is about, empty for the list, which costs no round trip.
	Showing string
	// Cards are every server this account talks to, filled in by whichever plugin answers for each.
	Cards []ServerCard
	// Services are the calendar and contacts servers the account names for itself.
	Services        alborz.Services
	HasHTTPPassword bool
	// Places are where the account's new calendars and address books can go (ADR 28).
	Places []Place
}

// Server groups, in the order the list shows them.
const (
	ServerMail    = "mail"
	ServerSending = "sending"
	ServerFilters = "filters"
	ServerDAV     = "dav"
)

var serverOrder = []string{ServerMail, ServerSending, ServerFilters, ServerDAV}

var serverPages = map[string]bool{ServerMail: true, ServerDAV: true}

// ServerCard is one upstream's card; Rows are the label/value maps the shared card renders.
type ServerCard struct {
	// Group is the list row the card belongs to; several cards can share one.
	Group string
	Title string
	// Host is empty for collections kept here; Source is a translation key; Record is the SRV record it was found by.
	Host, Source, Record string
	Rows                 []map[string]any
	Abilities            []Ability
	Explained            []alborz.Explained
}

// SourceText says how alborz came to the server, in words.
func (c ServerCard) SourceText(t func(string) string) string {
	if c.Record != "" {
		return fmt.Sprintf(t(c.Source), c.Record)
	}
	return t(c.Source)
}

// Place is one place new collections can go.
type Place struct {
	ID, Label string
}

// AddPlace offers a place once, however many plugins know it.
func (d *ServersRenderData) AddPlace(id, label string) {
	for _, p := range d.Places {
		if p.ID == id {
			return
		}
	}
	d.Places = append(d.Places, Place{ID: id, Label: label})
}

// DefaultPlace is the account's default place, else the first offered.
func (d *ServersRenderData) DefaultPlace() string {
	for _, p := range d.Places {
		if p.ID == d.Services.Default {
			return p.ID
		}
	}
	if len(d.Places) > 0 {
		return d.Places[0].ID
	}
	return ""
}

var placeIDs = []string{"domain", "host", "own", "here"}

// ServerRow is one row of the list: a group of cards by its hosts.
type ServerRow struct {
	Title, Hosts, Source, Href string
}

// Rows are the list's rows, one per group that has a card.
func (d *ServersRenderData) Rows() []ServerRow {
	var rows []ServerRow
	for _, group := range serverOrder {
		row := ServerRow{Title: d.T("servers." + group)}
		var hosts []string
		found := false
		for _, c := range d.Cards {
			if c.Group != group {
				continue
			}
			found = true
			if c.Host != "" && !slices.Contains(hosts, c.Host) {
				hosts = append(hosts, c.Host)
			}
			if row.Source == "" && c.Source != "" {
				row.Source = c.SourceText(d.T)
			}
		}
		if !found {
			continue
		}
		row.Hosts = strings.Join(hosts, ", ")
		if serverPages[group] {
			row.Href = "/settings/servers/" + group + accountQuery(d.GlobalData.URLAccount)
		}
		rows = append(rows, row)
	}
	return rows
}

// Shown are the cards of the server the page is about.
func (d *ServersRenderData) Shown() []ServerCard {
	var shown []ServerCard
	for _, c := range d.Cards {
		if c.Group == d.Showing {
			shown = append(shown, c)
		}
	}
	return shown
}

func accountQuery(address string) string {
	if address == "" {
		return ""
	}
	return "?account=" + alborz.QueryValue(address)
}

func settingsRail(ctx *alborz.Context) map[string][]alborz.RailRow {
	path := ctx.Request().URL.Path
	// Under no account: what this browser keeps.
	rows := map[string][]alborz.RailRow{"": {
		{Label: ctx.T("settings.general"), Href: "/settings", Active: path == "/settings"},
		{Label: ctx.T("passkeys.title"), Href: "/settings/passkeys", Active: strings.HasPrefix(path, "/settings/passkeys")},
	}}
	for _, account := range ctx.Accounts() {
		scoped := ctx.Login != nil && account.Username == ctx.Login.Username()
		q := accountQuery(account.Username)
		rows[account.Username] = []alborz.RailRow{
			{Label: ctx.T("settings.account"), Href: "/settings/account" + q, Active: scoped && path == "/settings/account"},
			{Label: ctx.T("settings.signatures"), Href: "/signatures" + q, Active: scoped && strings.HasPrefix(path, "/signatures")},
			{Label: ctx.T("settings.servers"), Href: "/settings/servers" + q, Active: scoped && strings.HasPrefix(path, "/settings/servers")},
			{Label: ctx.T("sessions.title"), Href: "/settings/sessions" + q, Active: scoped && path == "/settings/sessions"},
		}
	}
	return rows
}

func handleSignatures(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	return ctx.Render(http.StatusOK, "signatures.html", &SignaturesRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.signatures")),
		Settings:       settings,
		Rail:           settingsRail(ctx),
	})
}

func handleSignatureForm(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	data := &SignatureRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.signatures")),
		Rail:           settingsRail(ctx),
	}
	if name := ctx.Param("name"); name != "" {
		found, ok := settings.signatureNamed(name)
		if !ok {
			return alborz.NotFound("notfound.signature", name)
		}
		data.Editing, data.Was = found, found.Name
	}
	return ctx.Render(http.StatusOK, "signature-edit.html", data)
}

func handleSignatureSave(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	name := strings.TrimSpace(ctx.FormValue("name"))
	text := strings.TrimRight(ctx.FormValue("text"), "\r\n")
	was := ctx.FormValue("was")
	editing := Signature{Name: name, Text: text}
	render := func(message string) error {
		return ctx.Render(http.StatusUnprocessableEntity, "signature-edit.html", &SignatureRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.signatures")).Refused(message),
			Editing:        editing,
			Was:            was,
			Rail:           settingsRail(ctx),
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
			ctx.AccountPath("/signatures/"+url.PathEscape(name)),
			ctx.AccountPath("/signatures/create"))
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/signatures"))
}

func handleSignatureDelete(ctx *alborz.Context) error {
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
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.SaveRefusal(err)})
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/signatures"))
}

func handleSignatureDefault(ctx *alborz.Context) error {
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
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.SaveRefusal(err)})
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/signatures"))
}

func (p *plugin) handleSettings(ctx *alborz.Context) error {
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
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("notice.noanswer")})
		storage = SettingsStorage{}
	}

	reject := func(message string) error {
		return ctx.Render(http.StatusUnprocessableEntity, "settings-account.html", &SettingsRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.account")).Refused(message),
			Settings:       settings,
			Mailboxes:      mailboxes,
			Subscriptions:  Subscriptions(settings.Subscriptions),
			Storage:        storage,
			Rail:           settingsRail(ctx),
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
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings/account"))
	}

	// The suggestion costs a fetch, so only when nothing is set.
	guess := ""
	if settings.TrustedAuthServ == "" {
		guess = p.SuggestAuthServ(ctx)
	}
	return ctx.Render(http.StatusOK, "settings-account.html", &SettingsRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.account")),
		AuthServGuess:  guess,
		Settings:       settings,
		Mailboxes:      mailboxes,
		Subscriptions:  Subscriptions(settings.Subscriptions),
		Storage:        storage,
		Rail:           settingsRail(ctx),
	})
}

func (p *plugin) handleForget(ctx *alborz.Context) error {
	store, ok := ctx.Login.Store().(alborz.Settings)
	if !ok {
		// Nothing else outlives the process to forget.
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.T("settings.forgotten")})
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings/account"))
	}
	if err := store.Forget(ctx.Request().Context()); err != nil {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.SaveRefusal(err)})
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings/account"))
	}
	p.accountChanged(ctx.Login.Username())
	ctx.Notify(alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.T("settings.forgotten")})
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings/account"))
}

func mailCards(ctx *alborz.Context, showing string) ([]ServerCard, error) {
	_, domain, _ := strings.Cut(ctx.Login.Username(), "@")
	up := ctx.Server.UpstreamsFor(domain)
	source := func(record string) (string, string) {
		if record != "" {
			return "servers.fromsrv", record
		}
		return "servers.fromconfig", ""
	}
	card := func(group, title, host, record string) ServerCard {
		src, rec := source(record)
		return ServerCard{Group: group, Title: ctx.T(title), Host: host, Source: src, Record: rec}
	}
	cards := []ServerCard{
		card(ServerMail, "servers.mail", up.IMAP, up.IMAPFound),
		card(ServerSending, "servers.sending", up.SMTP, up.SMTPFound),
	}
	if up.Sieve != "" {
		cards = append(cards, card(ServerFilters, "servers.filters", up.Sieve, up.SieveFound))
	}
	if showing != ServerMail {
		return cards, nil
	}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return nil, err
	}
	var agent string
	var abilityList []Ability
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		agent = serverAgent(c)
		abilityList = abilities(c, settings)
		return nil
	})
	if err != nil {
		return nil, err
	}
	info := serverInfo(ctx, agent, abilityList)
	cards[0].Rows = []map[string]any{
		{"label": ctx.T("settings.serverhost"), "value": info.IMAP},
		{"label": ctx.T("settings.serversource"), "value": cards[0].SourceText(ctx.T)},
		{"label": ctx.T("settings.serverconnection"), "value": ctx.T("settings.conn" + up.IMAPSecurity)},
		{"label": ctx.T("settings.serversoftware"), "value": info.Agent},
	}
	cards[0].Abilities, cards[0].Explained = info.Abilities, info.Explained
	return cards, nil
}

func handleServers(ctx *alborz.Context) error {
	cards, err := mailCards(ctx, "")
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "servers.html", &ServersRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.servers")),
		Rail:           settingsRail(ctx),
		Cards:          cards,
	})
}

func handleServer(ctx *alborz.Context) error {
	showing := ctx.Param("server")
	if !serverPages[showing] {
		return alborz.NotFound("notfound.server")
	}
	cards, err := mailCards(ctx, showing)
	if err != nil {
		return err
	}
	data := &ServersRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("servers." + showing)),
		Rail:           settingsRail(ctx),
		Showing:        showing,
		Cards:          cards,
	}
	if showing != ServerDAV {
		return ctx.Render(http.StatusOK, "server.html", data)
	}
	if data.HasHTTPPassword, err = ctx.Login.HasHTTPPassword(ctx.Request().Context()); err != nil {
		return err
	}
	if data.Services, err = ctx.Login.Services(ctx.Request().Context()); err != nil {
		return err
	}
	if ctx.Request().Method != http.MethodPost {
		return ctx.Render(http.StatusOK, "server.html", data)
	}

	// What was typed is what the form shows again when one is refused.
	named := alborz.Services{
		CalDAV:   strings.TrimSpace(ctx.FormValue("caldav_url")),
		CardDAV:  strings.TrimSpace(ctx.FormValue("carddav_url")),
		Username: strings.TrimSpace(ctx.FormValue("dav_username")),
	}
	if d := ctx.FormValue("dav_default"); slices.Contains(placeIDs, d) {
		named.Default = d
	}
	for _, server := range []string{named.CalDAV, named.CardDAV} {
		if server == "" {
			continue
		}
		if err := ctx.Server.CheckServiceURL(ctx.Request().Context(), server); err != nil {
			data.Services = named
			data.Refused(fmt.Sprintf(ctx.T(serviceRefusals[err]), server))
			return ctx.Render(http.StatusUnprocessableEntity, "server.html", data)
		}
	}
	// An empty field leaves the kept password alone; only the reset box lets it go.
	refuse := func(err error) error {
		data.Services = named
		data.Refused(ctx.SaveRefusal(err))
		return ctx.Render(http.StatusUnprocessableEntity, "server.html", data)
	}
	if ctx.FormValue("http_password_reset") != "" {
		err = ctx.Login.SetHTTPPassword(ctx.Request().Context(), "")
	} else if p := ctx.FormValue("http_password"); p != "" {
		err = ctx.Login.SetHTTPPassword(ctx.Request().Context(), p)
	}
	if err != nil {
		return refuse(err)
	}
	if named != data.Services {
		if err := ctx.Login.SetServices(ctx.Request().Context(), named); err != nil {
			return refuse(err)
		}
	}
	// The cache was read with the servers or password just left.
	ctx.Server.ForgetAccount(ctx.Login.Username())
	ctx.PutNotice(ctx.T("notice.serverssaved"))
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings/servers/"+ServerDAV))
}

func handleLanguage(ctx *alborz.Context) error {
	if err := ctx.SetLanguage(ctx.FormValue("language")); err != nil {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.SaveRefusal(err)})
	} else {
		ctx.Quiet()
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr("/"))
}

func handleProfile(ctx *alborz.Context) error {
	render := func(status int, message string, p alborz.Profile) error {
		return ctx.Render(status, "settings.html", &ProfileRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.general")).Refused(message),
			Rail:           settingsRail(ctx),
			Theme:          p.Display.Theme,
			Themes:         ctx.Themes(),
			AlignByScript:  p.Display.AlignByScript,
			TextSize:       p.Display.TextSize,
			Reading:        p.Reading,
			Writing:        p.Writing,
			MaxPerPage:     maxMessagesPerPage,
			LockMinutes:    lockMinutes(ctx.Session().Lock()),
		})
	}
	shown := ctx.Profile()
	shown.Reading = ctx.Reading()
	shown.Display.Theme, shown.Display.TextSize = ctx.Theme(), ctx.TextSize()
	if ctx.Request().Method != http.MethodPost {
		return render(http.StatusOK, "", shown)
	}

	// An absent field keeps its value; a present empty one clears, except a checkbox, which sends nothing for off.
	sent, err := ctx.FormParams()
	if err != nil {
		return err
	}
	given := func(name string) (string, bool) {
		values, ok := sent[name]
		if !ok || len(values) == 0 {
			return "", false
		}
		return values[0], true
	}

	p := shown
	reading := &p.Reading
	if v, ok := given("messages_per_page"); ok {
		reading.MessagesPerPage, err = alborz.ReadInt(v)
		if err != nil || reading.MessagesPerPage <= 0 || reading.MessagesPerPage > maxMessagesPerPage {
			return render(http.StatusUnprocessableEntity, fmt.Sprintf(ctx.T("form.perpage"), maxMessagesPerPage), shown)
		}
	}
	if v, ok := given("timezone"); ok {
		reading.Timezone = v
	}
	reading.PreferHTML = ctx.FormValue("prefer_html") != ""
	if fdow, ok := given("first_day_of_week"); ok && fdow != "" {
		reading.FirstDayOfWeek, err = alborz.ReadInt(fdow)
		if err != nil || reading.FirstDayOfWeek < 0 || reading.FirstDayOfWeek > 6 {
			return render(http.StatusUnprocessableEntity, ctx.T("form.firstday"), shown)
		}
	}
	if v, ok := given("calendar"); ok {
		reading.Primary = alborz.CalendarNamed(v)
	}
	if v, ok := given("secondary"); ok {
		reading.Secondary = alborz.CalendarNamed(v)
	}
	if reading.Secondary == reading.Primary {
		reading.Secondary = ""
	}
	if v, ok := given("reply_position"); ok {
		p.Writing.ReplyBelowQuote = v == "below"
	}
	p.Writing.NoDirectionPart = ctx.FormValue("direction_part") == ""
	if v, ok := given("theme"); ok {
		p.Display.Theme = ""
		if slices.ContainsFunc(ctx.Themes(), func(t alborz.Theme) bool { return t.Name == v }) {
			p.Display.Theme = v
		}
	}
	if v, ok := given("text_size"); ok {
		p.Display.TextSize = ""
		if slices.Contains(alborz.TextSizes(), v) {
			p.Display.TextSize = v
		}
	}
	p.Display.AlignByScript = ctx.FormValue("align_script") != ""
	var lockAfter time.Duration
	if v, ok := given("lock_minutes"); ok {
		minutes, err := alborz.ReadInt(v)
		lockAfter = time.Duration(minutes) * time.Minute
		if err != nil || lockAfter < alborz.MinLockAfter {
			return render(http.StatusUnprocessableEntity, ctx.T("form.lockminutes"), shown)
		}
	}
	if err := ctx.SetProfile(p); err != nil {
		return render(http.StatusUnprocessableEntity, ctx.SaveRefusal(err), p)
	}
	if lockAfter > 0 {
		session := ctx.Session()
		session.SetLockAfter(lockAfter)
		if err := ctx.Server.Sessions.Save(session); err != nil {
			return err
		}
	}
	ctx.PutNotice(ctx.T("notice.settingssaved"))
	return ctx.Redirect(http.StatusFound, "/settings")
}

func handleProfileTake(ctx *alborz.Context) error {
	return answerProfile(ctx, ctx.TakeProfile, "profile.taken")
}

func handleProfileDiscard(ctx *alborz.Context) error {
	return answerProfile(ctx, ctx.KeepProfile, "profile.discarded")
}

func answerProfile(ctx *alborz.Context, answer func(*alborz.Login) error, done string) error {
	account := ctx.FormValue("account")
	login := ctx.LoginFor(account)
	if login == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
	}
	if err := answer(login); err != nil {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.SaveRefusal(err)})
	} else {
		ctx.PutNotice(fmt.Sprintf(ctx.T(done), account))
	}
	return ctx.Redirect(http.StatusFound, "/settings")
}

func lockMinutes(l alborz.Lock) int {
	if len(l.Passkeys) == 0 {
		return 0
	}
	return int(l.Wait() / time.Minute)
}
