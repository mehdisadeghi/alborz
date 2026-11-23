// Package web shows the time in the zones the reader keeps a clock for as a segment of the status
// bar, the page that chooses them, and a contact's time beside the zone on their card.
package web

import (
	"embed"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/addons/clock"
	"git.mehdix.org/alborz/internal/locale"
	contactsweb "git.mehdix.org/alborz/pim/contacts/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-vcard"
)

//go:embed pages
var pages embed.FS

// Mount offers the clocks at the start of the bar, the page that chooses their zones, and a contact's time.
func Mount(g *web.Group) {
	g.Group("", "", web.Personal).GET("zones", "/clock", func(ctx *web.Context) error { return render(ctx, http.StatusOK, "") })
	g.POST("zones", "/clock", handleChange)
	s := g.Server()
	if err := s.RegisterAddonBundle("clock", pages); err != nil {
		panic(err)
	}
	web.Extend(s, func(ctx *web.Context, data *contactsweb.AddressObjectRenderData) error {
		if loc, ok := clock.Zone(data.AddressObject.Card.PreferredValue(vcard.FieldTimezone)); ok {
			data.ZoneNow = now(ctx, loc, time.Now())
		}
		return nil
	})
	s.Status = append(s.Status, web.Segment{Name: "clock", Page: "clock.zones", Show: func(ctx *web.Context) (web.Shown, error) {
		return show(ctx), nil
	}})
}

// show is each clock's city and time; the zones and offsets are a hover away.
func show(ctx *web.Context) web.Shown {
	link := ctx.URL("clock.zones")
	at := time.Now()
	var parts, title []string
	for _, zone := range ctx.Profile().Clocks {
		loc, ok := clock.Named(zone)
		if !ok {
			continue
		}
		parts = append(parts, city(ctx, zone)+" "+clockIn(ctx, loc, at))
		// Isolated, as a title cannot hold a bdi.
		title = append(title, "\u2068"+zone+"\u2069 · \u2068"+now(ctx, loc, at)+"\u2069")
	}
	if len(parts) == 0 {
		return web.Shown{Parts: []string{ctx.T("clock.choose")}, Link: link}
	}
	return web.Shown{Parts: parts, Title: strings.Join(title, "\n"), Link: link}
}

// city is the zone's city in the reader's language where the add-on names it, else as the zone writes it.
func city(ctx *web.Context, zone string) string {
	key := "clock.city." + zone
	if name := ctx.T(key); name != key {
		return name
	}
	return clock.City(zone)
}

// clockIn is the time at in loc, led by the weekday there when it is not the reader's.
func clockIn(ctx *web.Context, loc *time.Location, at time.Time) string {
	r := ctx.Dates()
	there, here := at.In(loc), r.In(at)
	r.Loc = loc
	if there.Weekday() == here.Weekday() {
		return r.Clock(at)
	}
	return ctx.T(fmt.Sprintf("calendar.weekdayshort.%d", there.Weekday())) + " " + r.Clock(at)
}

// now is the time in loc and its offset from UTC: "14:05 (UTC+02:00)".
func now(ctx *web.Context, loc *time.Location, at time.Time) string {
	sign, h, m := clock.Offset(at.In(loc))
	offset := ctx.Tf("clock.offset", sign+locale.Sprintf(ctx.Dates().Lang, "%02d:%02d", h, m))
	return ctx.Tf("clock.now", clockIn(ctx, loc, at), offset)
}

// Row is one clock as the page lists it.
type Row struct {
	Zone, City, Now string
}

// ZonesRenderData is the clocks in force and a zone being added.
type ZonesRenderData struct {
	web.BaseRenderData
	Rail  map[string][]web.RailRow
	Rows  []Row
	Typed string
}

func render(ctx *web.Context, status int, typed string) error {
	data := &ZonesRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("clock.status")),
		Rail:           web.SettingsRail(ctx),
		Typed:          typed,
	}
	at := time.Now()
	for _, zone := range ctx.Profile().Clocks {
		row := Row{Zone: zone, City: city(ctx, zone)}
		if loc, ok := clock.Named(zone); ok {
			row.Now = now(ctx, loc, at)
		}
		data.Rows = append(data.Rows, row)
	}
	if status != http.StatusOK {
		data.Refused(ctx.T("form.timezone"))
	}
	return ctx.Render(status, "clock/zones", data)
}

// handleChange adds the zone "add" names, or takes away the one "remove" names.
func handleChange(ctx *web.Context) error {
	notice := "clock.removed"
	edit := func(p *alborz.Profile) {
		p.Clocks = slices.DeleteFunc(p.Clocks, func(z string) bool { return z == ctx.FormValue("remove") })
	}
	if ctx.FormValue("remove") == "" {
		zone := strings.TrimSpace(ctx.FormValue("add"))
		if _, ok := clock.Named(zone); !ok {
			return render(ctx, http.StatusUnprocessableEntity, zone)
		}
		notice = "clock.added"
		edit = func(p *alborz.Profile) {
			if !slices.Contains(p.Clocks, zone) {
				p.Clocks = append(p.Clocks, zone)
			}
		}
	}
	if err := ctx.UpdateProfile(edit); err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.SaveRefusal(err)})
	} else {
		ctx.PutNotice(ctx.T(notice))
	}
	return ctx.Redirect(http.StatusFound, ctx.Path("clock.zones"))
}
