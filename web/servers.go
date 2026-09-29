package web

import "fmt"

// Ability is one capability named for what it means to the reader.
type Ability struct {
	Label string
	Hint  string
	Have  bool
	// Stated marks an ability the reader claimed in settings rather than the server announced.
	Stated bool
}

// Server groups: a card's row in the list, and the page about them.
const (
	ServerMail    = "mail"
	ServerSending = "sending"
	ServerFilters = "filters"
	ServerDAV     = "dav"
)

// ServerCard is one upstream's card; Rows are the label/value maps the shared card renders.
type ServerCard struct {
	// Group is the list row the card belongs to; several cards can share one.
	Group string
	Title string
	// Host is empty for collections kept here; Source is a translation key; Record is the SRV record it was found by.
	Host, Source, Record string
	Rows                 []map[string]any
	Abilities            []Ability
	Explained            []Explained
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

// Servers are the account's servers as a page about them shows them, each feature adding its own.
type Servers struct {
	// Showing is the group the page is about, empty for the list, which costs no round trip.
	Showing string
	Cards   []ServerCard
	// Places are where the account's new calendars and address books can go (ADR 28).
	Places []Place
}

// Servers asks every feature for its servers, as the page about showing is shown, so a saved form asks none of them.
func (ctx *Context) Servers(showing string) (*Servers, error) {
	servers := &Servers{Showing: showing}
	for _, add := range ctx.Server.OnServers {
		if err := add(ctx, servers); err != nil {
			return nil, err
		}
	}
	return servers, nil
}

// AddPlace offers a place once, however many features know it.
func (s *Servers) AddPlace(id, label string) {
	for _, p := range s.Places {
		if p.ID == id {
			return
		}
	}
	s.Places = append(s.Places, Place{ID: id, Label: label})
}

// Shown are the cards of the group the page is about.
func (s *Servers) Shown() []ServerCard {
	var shown []ServerCard
	for _, c := range s.Cards {
		if c.Group == s.Showing {
			shown = append(shown, c)
		}
	}
	return shown
}
