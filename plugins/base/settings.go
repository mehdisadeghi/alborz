package alborzbase

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const settingsKey = "base.settings"

func init() {
	alborz.KeepKey(settingsKey)
}

const (
	maxMessagesPerPage = 100
	// defaultMessagesPerPage is what a reader who has chosen nothing
	// gets; the reading settings define it, and it is named here so the
	// warming code that has no context can use it too.
	defaultMessagesPerPage = alborz.DefaultMessagesPerPage
	maxSignature           = 2048
	maxDownloadName        = 80
	maxSignatures          = 20
	maxSignatureName       = 60
	maxFullName            = 512
)

// Signature is one of an account's sign-offs: a name to pick it by and
// the text that goes under the "-- " delimiter (RFC 3676 4.3).
type Signature struct {
	Name string
	Text string
}

type Settings struct {
	// MessagesPerPage, the timezone, the first day of the week and
	// whether HTML is preferred moved to the visit: they are what a
	// person reads by, and a merged page cannot say which account they
	// would belong to. See alborz.Reading.
	// Signatures belong to the account rather than to an identity: which
	// persona an address writes as is the writer's to decide per message,
	// and a rule mapping the two would be wrong as often as it was right.
	Signatures []Signature
	// DefaultSignature names the one a new message starts with; empty
	// means none.
	DefaultSignature string
	From             string
	// Identities are the other addresses this mailbox may send as, one
	// per line, each a bare address or a "Name <address>" pair. The
	// account's own address is always offered and is not listed here.
	Identities    []string
	Subscriptions []string

	// TrustedAuthServ is the authserv-id of the server that takes
	// delivery for this account - what it calls itself in the
	// Authentication-Results header it writes (RFC 8601). Only that
	// server's verdict is read, because every other instance of the
	// header was written by somebody upstream, possibly the sender.
	// Empty means the id observed on the account's own recent
	// deliveries is used (SuggestAuthServ), which is not a guess: it
	// is what the delivering hop wrote on mail the sender had no hand
	// in. Naming one here pins it.
	TrustedAuthServ string

	// IndexedSearch states that the server keeps a full-text index it
	// does not announce. Dovecot advertises SEARCH=FUZZY only for a
	// backend that flags fuzzy matching, which Solr does and xapian
	// and flatcurve do not, so an indexed server usually looks like an
	// unindexed one. An advertised capability counts on its own; this
	// is the reader vouching where the server is silent.
	IndexedSearch bool

	// ReplyBelowQuote puts the reply after the quoted message, the way a
	// mailing list expects it, instead of before. Stored positively:
	// the zero value keeps the reply on top, which is what a mail client
	// has always done here and what most correspondents expect.
	ReplyBelowQuote bool

	// SendHTML adds an HTML part to outgoing mail that says nothing but
	// which way each paragraph runs. Off by default: mailing lists ask
	// for plain text and some refuse multipart/alternative outright, so
	// an account that talks to lists wants it off and one that writes
	// Persian to Gmail wants it on.
	SendHTML bool
}

func LoadSettings(s alborz.Store) (*Settings, error) {
	settings := &Settings{}
	if err := s.Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	if key, limit := settings.check(); key != "" {
		return nil, fmt.Errorf("stored settings break %s (%d)", key, limit)
	}
	return settings, nil
}

// check reports the first rule the settings break as the key of the
// message that says so and the limit that message names; the key is
// empty when they hold.
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

// signatureNamed finds a signature by name; the second result is false
// when nothing carries that name, which is what a stale choice looks
// like after the signature it named was deleted.
func (s *Settings) signatureNamed(name string) (Signature, bool) {
	for _, sig := range s.Signatures {
		if sig.Name == name {
			return sig, true
		}
	}
	return Signature{}, false
}

// serviceRefusals says in the reader's words why a server was refused.
var serviceRefusals = map[error]string{
	alborz.ErrServiceNotURL:   "settings.servernoturl",
	alborz.ErrServiceNotHTTPS: "settings.servernothttps",
	alborz.ErrServiceNoHost:   "settings.servernohost",
	alborz.ErrServicePrivate:  "settings.serverprivate",
}

type SettingsRenderData struct {
	alborz.BaseRenderData
	// AuthServGuess is what this account's server appears to call itself
	// in the verdicts it writes, offered for confirmation. Empty when
	// the mail seen does not agree on one, because a close race is a
	// guess and a guess here is worse than nothing.
	AuthServGuess string
	Mailboxes     []MailboxInfo
	Settings      *Settings
	Subscriptions Subscriptions
	// Kept says where this account's settings are written and what is
	// written there.
	Kept KeptInfo
	Rail map[string][]alborz.RailRow
}

// KeptInfo says where an account's settings live. A server with no
// METADATA cannot hold them, and then alborz does: the page says which,
// names the host, and lists what is there, because settings written
// somewhere the reader was never told about are settings they cannot
// take back.
type KeptInfo struct {
	OnServer bool
	Host     string
	Entries  []string
	// Reading says what the reader reads by is kept under this account
	// too, which alborz holds itself whatever the server can do.
	Reading bool
}

func keptInfo(ctx *alborz.Context) (KeptInfo, error) {
	store, ok := ctx.Session.Store().(alborz.KeptStore)
	if !ok {
		return KeptInfo{}, nil
	}
	entries, err := store.Entries()
	if err != nil {
		return KeptInfo{}, err
	}
	_, host, _ := strings.Cut(ctx.Origin(), "://")
	if store.OnServer() {
		_, domain, _ := strings.Cut(ctx.Session.Username(), "@")
		host = ctx.Server.UpstreamsFor(domain).IMAP
	}
	_, reading := ctx.Server.Visits.LoadReading(ctx.Session.Username())
	return KeptInfo{OnServer: store.OnServer(), Host: host, Entries: entries, Reading: reading}, nil
}

// ServerInfo is what alborz can say about the account's upstreams
// without asking for anything it does not already have a connection to.
type ServerInfo struct {
	IMAP  string
	SMTP  string
	Sieve string
	// Agent is what the IMAP server calls itself (RFC 2971 ID). Empty
	// where the server does not advertise the extension, which many do
	// not; it is the server's own claim, not alborz's.
	Agent string
	// Abilities are the capabilities that change what alborz does, so
	// the page explains its own behaviour rather than listing a
	// protocol.
	Abilities []Ability
	// Explained is the same list as prose, for the disclosure that
	// works where a tooltip does not.
	Explained []alborz.Explained
}

// Ability is one capability named for what it means to the reader, with
// a line saying what it changes: the name alone is protocol jargon.
type Ability struct {
	Label string // translation key
	Hint  string // translation key
	Have  bool
	// Stated marks an ability the reader claimed in settings rather
	// than one the server announced, so the page says which it was.
	Stated bool
}

// abilities reports the capabilities that decide how alborz behaves.
// The raw CAPABILITY line is not shown: a reader wants to know whether
// sorting happens on the server, not that SORT=DISPLAY exists.
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

// ReadingRenderData is the settings page that belongs to nobody's
// account: what this browser reads by, and the choices the browser
// itself keeps.
type ReadingRenderData struct {
	alborz.BaseRenderData
	Rail  map[string][]alborz.RailRow
	Theme string
	// Themes are the overlays on offer, which a deployment adds to by
	// dropping a stylesheet beside the built-in ones.
	Themes        []alborz.Theme
	AlignByScript bool
	TextSize      string

	Reading    alborz.Reading
	MaxPerPage int
	// Anchor is the account these settings are kept on, empty for
	// none, and Accounts are the ones that could hold them.
	Anchor   string
	Accounts []alborz.Account
	// LockMinutes is how long this browser waits before it locks, zero
	// when it has no passkey and does not lock.
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

// serverAgent asks the IMAP server what it is (RFC 2971). A server
// without the extension answers nothing, which is not an error: the
// page simply has one fewer thing to say. The exchange is one round trip
// on a connection already open.
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

// serverInfo names where the account's mail lives. The hosts come from
// the configuration or from SRV discovery; nothing is guessed.
func serverInfo(ctx *alborz.Context, agent string, abilities []Ability) ServerInfo {
	_, domain, _ := strings.Cut(ctx.Session.Username(), "@")
	up := ctx.Server.UpstreamsFor(domain)
	explained := make([]alborz.Explained, 0, len(abilities))
	for _, a := range abilities {
		explained = append(explained, alborz.Explained{
			Term: ctx.T(a.Label), Hint: ctx.T(a.Hint)})
	}
	return ServerInfo{IMAP: up.IMAP, SMTP: up.SMTP, Sieve: up.Sieve,
		Agent: agent, Abilities: abilities, Explained: explained}
}

// SignaturesRenderData is the signature list, marking the one a new
// message starts with.
type SignaturesRenderData struct {
	alborz.BaseRenderData
	Settings *Settings
	Rail     map[string][]alborz.RailRow
}

// SignatureRenderData is one signature's form, new or existing.
type SignatureRenderData struct {
	alborz.BaseRenderData
	Editing Signature
	// Was is the name the form started with, so a rename replaces rather
	// than duplicates; empty when adding.
	Was  string
	Rail map[string][]alborz.RailRow
}

// ServersRenderData is what answers for the account: where its mail
// lives and what the software there can do. A deployment is not the
// one the reader is used to, and it is the first thing a bug report
// has to say.
type ServersRenderData struct {
	alborz.BaseRenderData
	Rail map[string][]alborz.RailRow
	// Showing is the server the page is about, empty for the list: a
	// card asks its server what it is only on its own page, so the list
	// costs no round trip.
	Showing string
	// Cards are every server this account talks to, filled in by
	// whichever plugin answers for each. Mail is not the only thing
	// with an upstream, and a calendar that behaves oddly is somebody
	// else's deployment too.
	Cards []ServerCard
	// The calendar and contacts servers the account names for itself,
	// whether it keeps a password of their own, and what was refused.
	Services        alborz.Services
	HasHTTPPassword bool
	// Places are where the account's new calendars and address books
	// can go, filled in by the DAV plugins with their sources and Alborz
	// (ADR 28); the account's default is one of them.
	Places []Place
}

// The servers an account has, in the order the list shows them. A
// server with a page of its own is one with more to say than its host.
const (
	ServerMail    = "mail"
	ServerSending = "sending"
	ServerFilters = "filters"
	ServerDAV     = "dav"
)

var serverOrder = []string{ServerMail, ServerSending, ServerFilters, ServerDAV}

var serverPages = map[string]bool{ServerMail: true, ServerDAV: true}

// ServerCard is one upstream's card. Rows carry the "label" and "value"
// pairs the shared card renders, which is why they are maps rather than
// a type of their own.
type ServerCard struct {
	// Group is the list row the card belongs to; several cards can,
	// as the calendar and the contacts server do.
	Group string
	Title string
	// Host is where the server is, empty for the collections kept here;
	// Source is how alborz came to it, a translation key, and Record
	// the SRV record it names where the server was found by one.
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

// Place is one place new collections can go: a source's id and what
// the reader knows it by.
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

// DefaultPlace is the one the page shows chosen: the account's, else
// the first offered.
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

// placeIDs are the places an account's default can name (ADR 28).
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

// settingsRail lists the places in this section. What the reader reads
// by names no account and heads the rail; what an account is follows,
// once per account, because those are per account by nature.
func settingsRail(ctx *alborz.Context) map[string][]alborz.RailRow {
	path := ctx.Request().URL.Path
	// Under no account: what this browser keeps, whichever accounts it
	// is signed into.
	rows := map[string][]alborz.RailRow{"": {
		{Label: ctx.T("settings.general"), Href: "/settings", Active: path == "/settings"},
		{Label: ctx.T("passkeys.title"), Href: "/settings/passkeys", Active: strings.HasPrefix(path, "/settings/passkeys")},
	}}
	for _, account := range ctx.Accounts() {
		scoped := ctx.Session != nil && account.Username == ctx.Session.Username()
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

// handleSignatures keeps signatures out of the settings pane: they are
// prose a person writes, not a preference to be set. The page lists what
// exists and writes one at a time, because deleting is an action rather
// than a box to tick and then save.
func handleSignatures(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	return ctx.Render(http.StatusOK, "signatures.html", &SignaturesRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.signatures")),
		Settings:       settings,
		Rail:           settingsRail(ctx),
	})
}

// handleSignatureForm opens a signature to write: a new one, or the one
// the URL names.
func handleSignatureForm(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Session.Store())
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
	settings, err := LoadSettings(ctx.Session.Store())
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

	// A rename keeps the entry's place in the list, and keeps being the
	// default if it was one.
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
	if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
	}
	if !replaced {
		ctx.Made(ctx.T("notice.signaturecreated"), name,
			ctx.AccountPath("/signatures/"+url.PathEscape(name)),
			ctx.AccountPath("/signatures/create"))
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/signatures"))
}

// handleSignatureDelete removes one. It is its own route because a
// deletion is an action taken on a thing, not a field of a form that
// saves everything at once.
func handleSignatureDelete(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Session.Store())
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
	if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/signatures"))
}

// handleSignatureDefault sets which signature a new message starts with.
// It is a preference and saves on its own, so choosing one never depends
// on what the form below happens to hold.
func handleSignatureDefault(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	// An empty name clears the default: a message then starts bare.
	chosen := ctx.FormValue("name")
	if _, ok := settings.signatureNamed(chosen); !ok {
		chosen = ""
	}
	settings.DefaultSignature = chosen
	if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/signatures"))
}

func handleSettings(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Session.Store())
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
	// What the account keeps and where is one section of this page. A
	// server that will not answer for it - METADATA refused, a depth it
	// does not support - says so on the page; it is not a reason to
	// refuse the settings.
	kept, err := keptInfo(ctx)
	if err != nil {
		ctx.Logger().Printf("settings: listing what %s keeps: %v", ctx.Session.Username(), err)
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("notice.noanswer")})
		kept = KeptInfo{}
	}

	// The form answers its own invalid input, on the page it was typed
	// on. Digits are read as they were typed: a page that counts in
	// Persian digits invites them back in its number fields.
	reject := func(message string) error {
		return ctx.Render(http.StatusUnprocessableEntity, "settings-account.html", &SettingsRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.account")).Refused(message),
			Settings:       settings,
			Mailboxes:      mailboxes,
			Subscriptions:  Subscriptions(settings.Subscriptions),
			Kept:           kept,
			Rail:           settingsRail(ctx),
		})
	}

	if ctx.Request().Method == http.MethodPost {
		settings.From = ctx.FormValue("from")
		settings.TrustedAuthServ = strings.TrimSpace(ctx.FormValue("trusted_authserv"))
		settings.IndexedSearch = ctx.FormValue("indexed_search") != ""
		settings.Identities = parseIdentities(ctx.FormValue("identities"))
		settings.ReplyBelowQuote = ctx.FormValue("reply_position") == "below"
		settings.SendHTML = ctx.FormValue("send_html") != ""

		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		settings.Subscriptions = params["subscriptions"]

		if key, limit := settings.check(); key != "" {
			return reject(fmt.Sprintf(ctx.T(key), limit))
		}
		if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
			return fmt.Errorf("failed to save settings: %w", err)
		}
		accountChanged(ctx.Session.Username())
		ctx.PutNotice(ctx.T("notice.settingssaved"))
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings/account"))
	}

	// Only when nothing is set: with a value in hand there is nothing to
	// suggest, and the sample costs a fetch.
	guess := ""
	if settings.TrustedAuthServ == "" {
		guess = SuggestAuthServ(ctx)
	}
	return ctx.Render(http.StatusOK, "settings-account.html", &SettingsRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.account")),
		AuthServGuess:  guess,
		Settings:       settings,
		Mailboxes:      mailboxes,
		Subscriptions:  Subscriptions(settings.Subscriptions),
		Kept:           kept,
		Rail:           settingsRail(ctx),
	})
}

// handleForget empties what alborz wrote for this account, wherever it
// went. Written settings the reader cannot remove are settings they do
// not own, and on a server it is the reader's mailbox that is holding
// them.
func handleForget(ctx *alborz.Context) error {
	if err := ctx.Server.Visits.ForgetReading(ctx.Session.Username()); err != nil {
		return err
	}
	store, ok := ctx.Session.Store().(alborz.KeptStore)
	if !ok {
		// Nothing else outlives the process to forget.
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.T("settings.forgotten")})
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings/account"))
	}
	if err := store.Forget(); err != nil {
		return err
	}
	accountChanged(ctx.Session.Username())
	ctx.Notify(alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.T("settings.forgotten")})
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings/account"))
}

// mailCards are the servers the deployment names for the account's
// domain; on its own page the mail server is asked what it is.
func mailCards(ctx *alborz.Context, showing string) ([]ServerCard, error) {
	_, domain, _ := strings.Cut(ctx.Session.Username(), "@")
	up := ctx.Server.UpstreamsFor(domain)
	// Where each came from: an SRV record, or the deployment naming it.
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
	settings, err := LoadSettings(ctx.Session.Store())
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

// handleServer is one server's page: what it says about itself, and for
// the calendar and contacts servers the ones the account names.
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
	if data.HasHTTPPassword, err = ctx.Session.HasHTTPPassword(); err != nil {
		return err
	}
	if data.Services, err = ctx.Session.Services(); err != nil {
		return err
	}
	if ctx.Request().Method != http.MethodPost {
		return ctx.Render(http.StatusOK, "server.html", data)
	}

	// The account's own servers. What was typed is what the form shows
	// again when one is refused.
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
	// An empty field leaves the kept password alone; the box is how it
	// is let go of, so nobody loses it by saving the page.
	if ctx.FormValue("http_password_reset") != "" {
		err = ctx.Session.SetHTTPPassword("")
	} else if p := ctx.FormValue("http_password"); p != "" {
		err = ctx.Session.SetHTTPPassword(p)
	}
	if err != nil {
		return err
	}
	if named != data.Services {
		if err := ctx.Session.SetServices(named); err != nil {
			return err
		}
	}
	// What is cached was read with the servers or the password just
	// left, and a refusal said about them is no longer news.
	ctx.Server.ForgetAccount(ctx.Session.Username())
	ctx.PutNotice(ctx.T("notice.serverssaved"))
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings/servers/"+ServerDAV))
}

// handleLanguage sets the interface language and returns to the page it
// was chosen from. It is its own route because the choice takes effect
// at once: a language behind a Save button is a language you have to
// read in the wrong one to change.
func handleLanguage(ctx *alborz.Context) error {
	ctx.SetLanguage(ctx.FormValue("language"))
	return ctx.Redirect(http.StatusFound, ctx.NextOr("/"))
}

// handleScheme forces light or dark, or gives way to the system, and
// returns to the page it was chosen from. Like the language, it takes
// effect on the click: a scheme behind a Save button is a scheme you
// have to stare at to change.
func handleScheme(ctx *alborz.Context) error {
	ctx.SetColorScheme(ctx.FormValue("scheme"))
	if ctx.Partial() {
		return ctx.Render(http.StatusOK, "scheme-toggle",
			alborz.NewBaseRenderData(ctx))
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr("/"))
}

// handleReadingSettings serves what the person at the screen reads by,
// which belongs to no account: the page size, the clock, the calendar
// and the theme. It names no account, so a server that is down cannot
// hold any of it hostage.
func handleReadingSettings(ctx *alborz.Context) error {
	render := func(status int, message string) error {
		return ctx.Render(status, "settings.html", &ReadingRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.general")).Refused(message),
			Rail:           settingsRail(ctx),
			Theme:          ctx.Theme(),
			Themes:         ctx.Themes(),
			AlignByScript:  ctx.AlignByScript(),
			TextSize:       ctx.TextSize(),
			Reading:        ctx.Reading(),
			MaxPerPage:     maxMessagesPerPage,
			Anchor:         ctx.Visit().Anchor(),
			Accounts:       ctx.Accounts(),
			LockMinutes:    lockMinutes(ctx.Visit().Lock()),
		})
	}
	if ctx.Request().Method != http.MethodPost {
		return render(http.StatusOK, "")
	}

	// A field the form did not carry is not a field set to nothing: it
	// was not on the page, and what it holds stays as it was. A field
	// that is there and empty is a choice, and clears. The exception is
	// a checkbox, where the browser sends nothing for "off" and absence
	// is the only way it has to say so.
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

	reading := ctx.Reading()
	if v, ok := given("messages_per_page"); ok {
		reading.MessagesPerPage, err = alborz.ReadInt(v)
		if err != nil || reading.MessagesPerPage <= 0 || reading.MessagesPerPage > maxMessagesPerPage {
			return render(http.StatusUnprocessableEntity, fmt.Sprintf(ctx.T("form.perpage"), maxMessagesPerPage))
		}
	}
	if v, ok := given("timezone"); ok {
		reading.Timezone = v
	}
	reading.PreferHTML = ctx.FormValue("prefer_html") != ""
	if fdow, ok := given("first_day_of_week"); ok && fdow != "" {
		reading.FirstDayOfWeek, err = alborz.ReadInt(fdow)
		if err != nil || reading.FirstDayOfWeek < 0 || reading.FirstDayOfWeek > 6 {
			return render(http.StatusUnprocessableEntity, ctx.T("form.firstday"))
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
	// The anchor is chosen from the accounts signed in; anything else
	// names nowhere, which is what a shared mailbox wants.
	if anchor, ok := given("anchor"); ok {
		if ctx.SessionFor(anchor) == nil {
			anchor = ""
		}
		if err := ctx.SetAnchor(anchor); err != nil {
			return err
		}
	}
	if err := ctx.SetReading(reading); err != nil {
		return err
	}

	if v, ok := given("theme"); ok {
		ctx.SetTheme(v)
	}
	if v, ok := given("text_size"); ok {
		ctx.SetTextSize(v)
	}
	ctx.SetAlignByScript(ctx.FormValue("align_script") != "")
	if v, ok := given("lock_minutes"); ok {
		minutes, err := alborz.ReadInt(v)
		if err != nil || time.Duration(minutes)*time.Minute < alborz.MinLockAfter {
			return render(http.StatusUnprocessableEntity, ctx.T("form.lockminutes"))
		}
		visit := ctx.Visit()
		visit.SetLockAfter(time.Duration(minutes) * time.Minute)
		if err := ctx.Server.Visits.Save(visit); err != nil {
			return err
		}
	}
	return ctx.Redirect(http.StatusFound, "/settings")
}

func lockMinutes(l alborz.Lock) int {
	if len(l.Passkeys) == 0 {
		return 0
	}
	return int(l.Wait() / time.Minute)
}
