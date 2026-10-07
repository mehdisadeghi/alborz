package web

import (
	"net/url"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
)

// TourStop is one stop of the tour (ADR 76).
type TourStop struct {
	// Name keys the stop's title and body, tour.<Name>.title and tour.<Name>.body, and names it in the URL.
	Name string
	// Target is the data-tour mark the stop points at; empty centres it.
	Target string
	// Route and Args are the page the stop shows on; an empty Route shows it on any.
	Route string
	Args  []any
}

// tourParam names the stop shown in a page's URL.
const tourParam = "tour"

var tourStops = []TourStop{
	{Name: "welcome"},
	{Name: "sections", Target: "sections", Route: "mail.mailbox", Args: []any{"INBOX"}},
	{Name: "search", Target: "search", Route: "mail.mailbox", Args: []any{"INBOX"}},
	{Name: "rail", Target: "rail", Route: "mail.mailbox", Args: []any{"INBOX"}},
	{Name: "accounts", Target: "accounts", Route: "mail.mailbox", Args: []any{"INBOX"}},
	{Name: "toolbar", Target: "toolbar", Route: "mail.mailbox", Args: []any{"INBOX"}},
	{Name: "rows", Target: "rows", Route: "mail.mailbox", Args: []any{"INBOX"}},
	{Name: "notices", Route: "mail.mailbox", Args: []any{"INBOX"}},
	{Name: "keys", Route: "mail.mailbox", Args: []any{"INBOX"}},
	{Name: "scheme", Target: "scheme", Route: "mail.mailbox", Args: []any{"INBOX"}},
	{Name: "mail", Target: "new", Route: "mail.mailbox", Args: []any{"INBOX"}},
	{Name: "calendar", Target: "calendar", Route: "calendar.month"},
	{Name: "tasks", Target: "tasks", Route: "calendar.tasks"},
	{Name: "contacts", Target: "new", Route: "contacts.list"},
	{Name: "journal", Target: "new", Route: "calendar.journal"},
	{Name: "filters", Target: "new", Route: "filters.list"},
	{Name: "settings", Target: "settings", Route: "settings.profile"},
	{Name: "done"},
}

// TourShown is a stop as the page draws it: the link that shows it, its number and its neighbours.
type TourShown struct {
	TourStop
	Href       string
	Number     int
	Back, Next *TourShown
}

// Tour is the tour drawn open at At.
type Tour struct {
	Stops []TourShown
	At    int
}

// tour is the tour a request names, nil for none; page is the request's URL without it.
func (ctx *Context) tour(page *url.URL, scope string) *Tour {
	at := slices.IndexFunc(tourStops, func(s TourStop) bool { return s.Name == ctx.QueryParam(tourParam) })
	if at < 0 {
		return nil
	}
	t := &Tour{At: at, Stops: make([]TourShown, len(tourStops))}
	for i, stop := range tourStops {
		t.Stops[i] = TourShown{TourStop: stop, Href: ctx.tourHref(stop, page, scope), Number: i + 1}
	}
	for i := range t.Stops {
		if i > 0 {
			t.Stops[i].Back = &t.Stops[i-1]
		}
		if i+1 < len(t.Stops) {
			t.Stops[i].Next = &t.Stops[i+1]
		}
	}
	return t
}

func (ctx *Context) tourHref(stop TourStop, page *url.URL, scope string) string {
	if stop.Route == "" {
		return withTour(page.EscapedPath(), page.RawQuery, stop.Name)
	}
	query := ""
	if scope != "" {
		query = "account=" + QueryValue(scope)
	}
	return withTour(ctx.Path(stop.Route, stop.Args...), query, stop.Name)
}

func withTour(path, rawQuery, name string) string {
	if rawQuery != "" {
		rawQuery += "&"
	}
	return path + "?" + rawQuery + tourParam + "=" + QueryValue(name)
}

// TourStart is the tour's first stop on page.
func TourStart(page *url.URL) string {
	return withTour(page.EscapedPath(), page.RawQuery, tourStops[0].Name)
}

// withoutTour is u without the tour's parameter: links a page builds from its own address do not carry it (ADR 76).
func withoutTour(u *url.URL) *url.URL {
	kept := slices.DeleteFunc(strings.Split(u.RawQuery, "&"), func(p string) bool {
		return p == "" || p == tourParam || strings.HasPrefix(p, tourParam+"=")
	})
	out := *u
	out.RawQuery = strings.Join(kept, "&")
	return &out
}

// OfferTour is where a sign-in lands: path, with the tour open at its first stop for a reader it never opened for (ADR 76).
func (ctx *Context) OfferTour(path string) (string, error) {
	if ctx.Profile().Toured {
		return path, nil
	}
	u, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	if err := ctx.UpdateProfile(func(p *alborz.Profile) { p.Toured = true }); err != nil {
		ctx.Logger().Printf("tour: no account kept the offer: %v", err)
		return path, nil
	}
	return TourStart(withoutTour(u)), nil
}
