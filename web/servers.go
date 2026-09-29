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

// Server groups, in the order the list shows them.
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
