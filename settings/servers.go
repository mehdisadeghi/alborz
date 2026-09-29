package settings

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

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
	Explained []web.Explained
}

// Ability is one capability named for what it means to the reader.
type Ability struct {
	Label string
	Hint  string
	Have  bool
	// Stated marks an ability the reader claimed in settings rather than the server announced.
	Stated bool
}

func abilities(c *imapclient.Client, settings *mail.Settings) []Ability {
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

func serverInfo(ctx *web.Context, agent string, abilities []Ability) ServerInfo {
	_, domain, _ := strings.Cut(ctx.Login.Username(), "@")
	up := ctx.Server.UpstreamsFor(domain)
	explained := make([]web.Explained, 0, len(abilities))
	for _, a := range abilities {
		explained = append(explained, web.Explained{
			Term: ctx.T(a.Label), Hint: ctx.T(a.Hint)})
	}
	return ServerInfo{IMAP: up.IMAP, SMTP: up.SMTP, Sieve: up.Sieve,
		Agent: agent, Abilities: abilities, Explained: explained}
}

// ServersRenderData is where the account's mail lives and what the software there can do.
type ServersRenderData struct {
	web.BaseRenderData
	Rail map[string][]web.RailRow
	// Showing is the server the page is about, empty for the list, which costs no round trip.
	Showing string
	// Cards are every server this account talks to, filled in by whichever plugin answers for each.
	Cards []ServerCard
	// Places are where the account's new calendars and address books can go (ADR 28).
	Places []Place
	// server is the address of a server group's own page.
	server func(group string) string
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
	Explained            []web.Explained
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
			row.Href = d.server(group) + mail.AccountQuery(d.GlobalData.URLAccount)
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

func mailCards(ctx *web.Context, showing string) ([]ServerCard, error) {
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
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
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

func handleServers(ctx *web.Context) error {
	cards, err := mailCards(ctx, "")
	if err != nil {
		return err
	}
	data := &ServersRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.servers")),
		Rail:           mail.SettingsRail(ctx),
		Cards:          cards,
		server:         func(group string) string { return ctx.Path("settings.server", group) },
	}
	return RenderServers(ctx, http.StatusOK, "settings/servers", data, data)
}

// RenderServers renders name with data, which shows servers. Other features' servers join as
// the page is shown, so a saved form asks none of them.
func RenderServers(ctx *web.Context, status int, name string, data web.RenderData, servers *ServersRenderData) error {
	for _, add := range ctx.Server.OnServers {
		if err := add(ctx, servers); err != nil {
			return err
		}
	}
	return ctx.Render(status, name, data)
}

func handleServer(ctx *web.Context) error {
	showing := ctx.Param("server")
	if !serverPages[showing] {
		return alborz.NotFound("notfound.server")
	}
	data, err := NewServersRenderData(ctx, showing)
	if err != nil {
		return err
	}
	return RenderServers(ctx, http.StatusOK, "settings/server", data, data)
}

// NewServersRenderData is the page about the servers of showing, with mail's among them.
func NewServersRenderData(ctx *web.Context, showing string) (*ServersRenderData, error) {
	cards, err := mailCards(ctx, showing)
	if err != nil {
		return nil, err
	}
	return &ServersRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("servers." + showing)),
		Rail:           mail.SettingsRail(ctx),
		Showing:        showing,
		Cards:          cards,
	}, nil
}
