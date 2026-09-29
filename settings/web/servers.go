package web

import (
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
	Abilities []web.Ability
	// Explained is the same list as prose.
	Explained []web.Explained
}

func abilities(c *imapclient.Client, settings *mail.Settings) []web.Ability {
	caps := c.Caps()
	indexed := caps.Has(imap.CapSearchFuzzy)
	return []web.Ability{
		{Label: "settings.abilitysort", Hint: "settings.abilitysorthint", Have: caps.Has(imap.CapSort)},
		{Label: "settings.abilitythread", Hint: "settings.abilitythreadhint", Have: caps.Has(imap.Cap("THREAD=REFERENCES"))},
		{Label: "settings.abilitysettings", Hint: "settings.abilitysettingshint", Have: caps.Has(imap.CapMetadata)},
		{Label: "settings.abilityquota", Hint: "settings.abilityquotahint", Have: caps.Has(imap.CapQuota)},
		{Label: "settings.abilitypush", Hint: "settings.abilitypushhint", Have: caps.Has(imap.CapIdle)},
		{Label: "settings.abilitycounts", Hint: "settings.abilitycountshint", Have: caps.Has(imap.CapListStatus)},
		{Label: "settings.abilityindex", Hint: "settings.abilityindexhint", Have: indexed || settings.IndexedSearch, Stated: !indexed && settings.IndexedSearch},
		{Label: "settings.abilityuidplus", Hint: "settings.abilityuidplushint", Have: caps.Has(imap.CapUIDPlus)},
		{Label: "settings.abilitysize", Hint: "settings.abilitysizehint", Have: caps.Has(imap.CapStatusSize)},
		{Label: "settings.abilityid", Hint: "settings.abilityidhint", Have: caps.Has(imap.CapID)},
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

func serverInfo(ctx *web.Context, agent string, abilities []web.Ability) ServerInfo {
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
	*web.Servers
	// server is the address of a server group's own page.
	server func(group string) string
}

var serverOrder = []string{web.ServerMail, web.ServerSending, web.ServerFilters, web.ServerDAV}

var serverPages = map[string]bool{web.ServerMail: true, web.ServerDAV: true}

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

// mailCards puts mail's servers among the account's.
func mailCards(ctx *web.Context, servers *web.Servers) error {
	_, domain, _ := strings.Cut(ctx.Login.Username(), "@")
	up := ctx.Server.UpstreamsFor(domain)
	source := func(record string) (string, string) {
		if record != "" {
			return "servers.fromsrv", record
		}
		return "servers.fromconfig", ""
	}
	card := func(group, title, host, record string) web.ServerCard {
		src, rec := source(record)
		return web.ServerCard{Group: group, Title: ctx.T(title), Host: host, Source: src, Record: rec}
	}
	cards := []web.ServerCard{
		card(web.ServerMail, "servers.mail", up.IMAP, up.IMAPFound),
		card(web.ServerSending, "servers.sending", up.SMTP, up.SMTPFound),
	}
	if up.Sieve != "" {
		cards = append(cards, card(web.ServerFilters, "servers.filters", up.Sieve, up.SieveFound))
	}
	if servers.Showing != web.ServerMail {
		servers.Cards = append(servers.Cards, cards...)
		return nil
	}
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	var agent string
	var abilityList []web.Ability
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		agent = serverAgent(c)
		abilityList = abilities(c, settings)
		return nil
	})
	if err != nil {
		return err
	}
	info := serverInfo(ctx, agent, abilityList)
	cards[0].Rows = []map[string]any{
		{"label": ctx.T("settings.serverhost"), "value": info.IMAP},
		{"label": ctx.T("settings.serversource"), "value": cards[0].SourceText(ctx.T)},
		{"label": ctx.T("settings.serverconnection"), "value": ctx.T("settings.conn" + up.IMAPSecurity)},
		{"label": ctx.T("settings.serversoftware"), "value": info.Agent},
	}
	cards[0].Abilities, cards[0].Explained = info.Abilities, info.Explained
	servers.Cards = append(servers.Cards, cards...)
	return nil
}

func handleServers(ctx *web.Context) error {
	servers, err := ctx.Servers("")
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "settings/servers", &ServersRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.servers")),
		Rail:           mail.SettingsRail(ctx),
		Servers:        servers,
		server:         func(group string) string { return ctx.Path("settings.server", group) },
	})
}

func handleServer(ctx *web.Context) error {
	showing := ctx.Param("server")
	if !serverPages[showing] {
		return alborz.NotFound("notfound.server")
	}
	servers, err := ctx.Servers(showing)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "settings/server", &ServersRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("servers." + showing)),
		Rail:           mail.SettingsRail(ctx),
		Servers:        servers,
	})
}
