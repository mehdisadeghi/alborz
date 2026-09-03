package alborzbase

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const settingsKey = "base.settings"

const (
	maxMessagesPerPage = 100
	maxSignature       = 2048
	// A header line may be 998 octets (RFC 5322 2.1.1); a body line in
	// the wild is longer, and a scanner that stops is a truncated file.
	maxMboxLine      = 1 << 20
	maxDownloadName  = 80
	maxSignatures    = 20
	maxSignatureName = 60
	maxFullName      = 512
)

// Signature is one of an account's sign-offs: a name to pick it by and
// the text that goes under the "-- " delimiter (RFC 3676 4.3).
type Signature struct {
	Name string
	Text string
}

type Settings struct {
	MessagesPerPage int
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
	Identities     []string
	Subscriptions  []string
	Timezone       string
	FirstDayOfWeek int // 0 = Sunday, 1 = Monday (default)

	// TrustedAuthServ is the authserv-id of the server that takes
	// delivery for this account - what it calls itself in the
	// Authentication-Results header it writes (RFC 8601). Only that
	// server's verdict is read, because every other instance of the
	// header was written by somebody upstream, possibly the sender.
	// Empty means no verdict is shown: a guess here would be worse than
	// silence, since the whole point is knowing who wrote the line.
	TrustedAuthServ string

	// Stored negated so the zero value keeps body search on by default.
	SearchHeadersOnly bool

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

	// PreferHTML opens a message at its HTML part where it has one.
	// Plain text is the default because it is the part a sender wrote
	// for reading rather than for looking at, and it carries no remote
	// content; an account whose correspondents send HTML that says
	// something the plain part does not wants the other order.
	PreferHTML bool
}

func LoadSettings(s alborz.Store) (*Settings, error) {
	settings := &Settings{
		MessagesPerPage: 50,
		FirstDayOfWeek:  1, // Monday
	}
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
	case s.MessagesPerPage <= 0 || s.MessagesPerPage > maxMessagesPerPage:
		return "form.perpage", maxMessagesPerPage
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
	Secondary     string // calendar system shown beside the Gregorian one
	MaxPerPage    int
	// Servers is where this account's mail actually lives, and what
	// software is answering. A deployment is not the one the reader is
	// used to, and it is the first thing a bug report has to say.
	Servers ServerInfo
	// HasHTTPPassword says a calendar and contacts password is kept,
	// which the form shows without ever showing the password.
	HasHTTPPassword bool
	Error           string
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
}

// abilities reports the capabilities that decide how alborz behaves.
// The raw CAPABILITY line is not shown: a reader wants to know whether
// sorting happens on the server, not that SORT=DISPLAY exists.
func abilities(c *imapclient.Client) []Ability {
	caps := c.Caps()
	return []Ability{
		{"settings.abilitysort", "settings.abilitysorthint", caps.Has(imap.CapSort)},
		{"settings.abilitythread", "settings.abilitythreadhint", caps.Has(imap.Cap("THREAD=REFERENCES"))},
		{"settings.abilitysettings", "settings.abilitysettingshint", caps.Has(imap.CapMetadata)},
		{"settings.abilityquota", "settings.abilityquotahint", caps.Has(imap.CapQuota)},
		{"settings.abilitypush", "settings.abilitypushhint", caps.Has(imap.CapIdle)},
		{"settings.abilityid", "settings.abilityidhint", caps.Has(imap.CapID)},
	}
}

// BrowserSettingsRenderData carries the choices stored in the browser
// rather than in any account.
type BrowserSettingsRenderData struct {
	alborz.BaseRenderData
	Language      string // explicit per-user choice, "" follows the browser
	Theme         string
	ColorScheme   string
	AccountColors bool
	AlignByScript bool
	TextSize      string
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

// SignaturesRenderData is the signature page: the list, which one a new
// message starts with, and the one being written if any.
type SignaturesRenderData struct {
	IMAPBaseRenderData
	Settings *Settings
	// Editing is the signature the form holds. Empty Name means the form
	// is adding rather than changing one.
	Editing Signature
	// Was is the name the form started with, so a rename replaces rather
	// than duplicates.
	Was   string
	Error string
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
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	editing, was := Signature{}, ""
	if name := ctx.QueryParam("edit"); name != "" {
		if found, ok := settings.signatureNamed(name); ok {
			editing, was = found, found.Name
		}
	}

	render := func(status int, message string) error {
		ibase.BaseRenderData.WithTitle(ctx.T("settings.signatures"))
		return ctx.Render(status, "signatures.html", &SignaturesRenderData{
			IMAPBaseRenderData: *ibase,
			Settings:           settings,
			Editing:            editing,
			Was:                was,
			Error:              message,
		})
	}

	if ctx.Request().Method != http.MethodPost {
		return render(http.StatusOK, "")
	}

	name := strings.TrimSpace(ctx.FormValue("name"))
	text := strings.TrimRight(ctx.FormValue("text"), "\r\n")
	was = ctx.FormValue("was")
	editing = Signature{Name: name, Text: text}
	switch {
	case name == "":
		return render(http.StatusUnprocessableEntity, ctx.T("form.signaturename"))
	case text == "":
		return render(http.StatusUnprocessableEntity, ctx.T("form.signaturetext"))
	case len(settings.Signatures) >= maxSignatures && was == "":
		return render(http.StatusUnprocessableEntity,
			fmt.Sprintf(ctx.T("form.signaturecount"), maxSignatures))
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
			return render(http.StatusUnprocessableEntity, ctx.T("form.signaturetaken"))
		}
		settings.Signatures = append(settings.Signatures, editing)
	}
	if key, limit := settings.check(); key != "" {
		return render(http.StatusUnprocessableEntity, fmt.Sprintf(ctx.T(key), limit))
	}
	if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
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
	chosen := ctx.FormValue("signature_default")
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
	var agent string
	var abilityList []Ability
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		mailboxes, err = listMailboxes(c)
		if err != nil {
			return err
		}
		agent = serverAgent(c)
		abilityList = abilities(c)
		return nil
	})
	if err != nil {
		return err
	}
	servers := serverInfo(ctx, agent, abilityList)
	hasHTTPPassword, err := ctx.Session.HasHTTPPassword()
	if err != nil {
		return err
	}

	// The form answers its own invalid input, on the page it was typed
	// on. Digits are read as they were typed: a page that counts in
	// Persian digits invites them back in its number fields.
	reject := func(message string) error {
		return ctx.Render(http.StatusUnprocessableEntity, "settings.html", &SettingsRenderData{
			BaseRenderData:  *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("nav.settings")),
			Settings:        settings,
			Mailboxes:       mailboxes,
			Subscriptions:   Subscriptions(settings.Subscriptions),
			Secondary:       ctx.SecondaryCalendar(),
			MaxPerPage:      maxMessagesPerPage,
			HasHTTPPassword: hasHTTPPassword,
			Error:           message,
		})
	}

	if ctx.Request().Method == http.MethodPost {
		settings.MessagesPerPage, err = strconv.Atoi(alborz.LatinDigits(ctx.FormValue("messages_per_page")))
		if err != nil {
			return reject(fmt.Sprintf(ctx.T("form.perpage"), maxMessagesPerPage))
		}
		settings.From = ctx.FormValue("from")
		settings.TrustedAuthServ = strings.TrimSpace(ctx.FormValue("trusted_authserv"))
		settings.Identities = parseIdentities(ctx.FormValue("identities"))
		settings.Timezone = ctx.FormValue("timezone")
		// An empty field leaves the kept password alone; the box is
		// how it is let go of, so nobody loses it by saving the page.
		if ctx.FormValue("http_password_reset") != "" {
			err = ctx.Session.SetHTTPPassword("")
		} else if p := ctx.FormValue("http_password"); p != "" {
			err = ctx.Session.SetHTTPPassword(p)
		}
		if err != nil {
			return err
		}
		settings.SearchHeadersOnly = ctx.FormValue("search_body") != "on"
		settings.ReplyBelowQuote = ctx.FormValue("reply_position") == "below"
		settings.PreferHTML = ctx.FormValue("prefer_html") != ""
		settings.SendHTML = ctx.FormValue("send_html") != ""
		if fdow := ctx.FormValue("first_day_of_week"); fdow != "" {
			settings.FirstDayOfWeek, err = strconv.Atoi(alborz.LatinDigits(fdow))
			if err != nil || settings.FirstDayOfWeek < 0 || settings.FirstDayOfWeek > 6 {
				return reject(ctx.T("form.firstday"))
			}
		}

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
		if err := ctx.SetSecondaryCalendar(ctx.FormValue("secondary")); err != nil {
			return fmt.Errorf("failed to save calendar choice: %w", err)
		}

		listings.evictAll(ctx.Session.Username())
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings"))
	}

	// Only when nothing is set: with a value in hand there is nothing to
	// suggest, and the sample costs a fetch.
	guess := ""
	if settings.TrustedAuthServ == "" {
		guess = SuggestAuthServ(ctx)
	}
	return ctx.Render(http.StatusOK, "settings.html", &SettingsRenderData{
		BaseRenderData:  *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("nav.settings")),
		AuthServGuess:   guess,
		Settings:        settings,
		Mailboxes:       mailboxes,
		Subscriptions:   Subscriptions(settings.Subscriptions),
		Secondary:       ctx.SecondaryCalendar(),
		MaxPerPage:      maxMessagesPerPage,
		Servers:         servers,
		HasHTTPPassword: hasHTTPPassword,
	})
}

// handleLanguage sets the interface language and returns to the page it
// was chosen from. It is its own route because the choice takes effect
// at once: a language behind a Save button is a language you have to
// read in the wrong one to change.
func handleLanguage(ctx *alborz.Context) error {
	ctx.SetLanguage(ctx.FormValue("language"))
	return ctx.Redirect(http.StatusFound, ctx.NextOr("/"))
}

// handleBrowserSettings serves the choices that live in the browser
// rather than in an account's store. It touches no account, so a server
// that is down cannot hold the language or the theme hostage.
func handleBrowserSettings(ctx *alborz.Context) error {
	if ctx.Request().Method == http.MethodPost {
		ctx.SetColorScheme(ctx.FormValue("color_scheme"))
		ctx.SetTheme(ctx.FormValue("theme"))
		ctx.SetLanguage(ctx.FormValue("language"))
		ctx.SetAccountColors(ctx.FormValue("account_colors") != "")
		ctx.SetAlignByScript(ctx.FormValue("align_script") != "")
		ctx.SetTextSize(ctx.FormValue("text_size"))
		return ctx.Redirect(http.StatusFound, "/settings/browser")
	}

	return ctx.Render(http.StatusOK, "settings-browser.html", &BrowserSettingsRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.forbrowser")),
		Language:       ctx.Language(),
		Theme:          ctx.Theme(),
		ColorScheme:    ctx.ColorScheme(),
		AccountColors:  ctx.AccountColors(),
		AlignByScript:  ctx.AlignByScript(),
		TextSize:       ctx.TextSize(),
	})
}
