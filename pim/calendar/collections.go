package calendar

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
)

func (p *plugin) invitations(ctx *web.Context) (*dav.RailInvitations, error) {
	return p.DAV.RailInvitations(ctx, p.collectionPage().Collection)
}

func (p *plugin) collectionPage() pim.Page {
	return pim.Page{
		Collection: "calendar.collection",
		List:       "calendar.month",
		Ext:        ".ics",
		Forget:     p.DAV.Forget,
		Show:       Show,
		Rail: func(ctx *web.Context, list string) (pim.Rail, error) {
			if list == ctx.Path("calendar.tasks") {
				return p.taskRail(ctx)
			}
			return p.eventRail(ctx)
		},
		Import: func(ctx *web.Context, path string, raw []byte) (int, error) {
			c, _, err := p.Open(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return 0, err
			}
			return Import(ctx.Request().Context(), c, path, raw)
		},
		Create: func(ctx *web.Context, list, name, place string) (string, error) {
			// A task list takes tasks; an imported calendar keeps events and tasks both.
			components := []string{"VEVENT", "VTODO"}
			if list == ctx.Path("calendar.tasks") {
				components = []string{"VTODO"}
			}
			return p.DAV.Create(ctx.Request().Context(), ctx.Login, name, dav.DefaultColor, place, components)
		},
		Export: func(ctx *web.Context, path string, from, to time.Time) ([]byte, error) {
			c, _, err := p.Open(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return nil, err
			}
			return Export(ctx.Request().Context(), c, path, from, to)
		},
		Lookup: func(ctx *web.Context, path string) (dav.Collection, func() int, string, string, error) {
			_, calendars, err := p.Open(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return dav.Collection{}, nil, "", "", err
			}
			info := dav.At(calendars, path)
			if info == nil {
				return dav.Collection{}, nil, "", "", alborz.NotFound("notfound.collection")
			}
			list := collectionList(ctx, *info)
			label := ctx.T("nav.calendar")
			if list == ctx.Path("calendar.tasks") {
				label = ctx.T("nav.tasks")
			}
			return *info, p.DAV.CountObjects(ctx.Request().Context(), ctx.Login, info.Path), list, label, nil
		},
	}
}

func collectionList(ctx *web.Context, info dav.Collection) string {
	if !SupportsEvent(info.Components) {
		return ctx.Path("calendar.tasks")
	}
	return ctx.Path("calendar.month")
}

func (p *plugin) createForm(ctx *web.Context) (pim.CreateForm, error) {
	forTasks := ctx.QueryParam("for") == "tasks"
	// Back to the rail that asked, when the new collection shows there.
	made := func(holds string) (string, string) {
		if holds == "tasks" {
			return ctx.T("notice.tasklistcreated"), ctx.Path("calendar.tasks")
		}
		return ctx.T("notice.calendarcreated"), ctx.Path("calendar.month")
	}
	if forTasks {
		rail, err := p.taskRail(ctx)
		return pim.CreateForm{Rail: rail, Title: ctx.T("tasks.newlist"), Section: ctx.T("nav.tasks"), List: ctx.Path("calendar.tasks"),
			Holds: "tasks", Made: made}, err
	}
	rail, err := p.eventRail(ctx)
	return pim.CreateForm{Rail: rail, Title: ctx.T("calendar.newcalendar"), Section: ctx.T("nav.calendar"), List: ctx.Path("calendar.month"),
		Holds: "both", OffersHolds: true, Made: made}, err
}

// SubscribeRenderData renders subscribe-calendar.html.
type SubscribeRenderData struct {
	web.BaseRenderData
	Rail     pim.Rail
	Accounts []web.Account
	Account  string
	Address  string
	Next     string
}

func handleSubscribe(p *plugin) func(*web.Context) error {
	return func(ctx *web.Context) error {
		rail, err := p.eventRail(ctx)
		if err != nil {
			return err
		}
		data := &SubscribeRenderData{
			Rail:           rail,
			BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("calendar.subscribe")),
			Accounts:       ctx.Accounts(),
			Account:        ctx.Login.Username(),
			Next:           ctx.FormValue("next"),
		}
		if ctx.Request().Method != http.MethodPost {
			return ctx.Render(http.StatusOK, "calendar/subscribe", data)
		}
		data.Address = strings.TrimSpace(ctx.FormValue("address"))
		if account := ctx.FormValue("account"); account != "" {
			data.Account = account
		}
		if data.Address == "" {
			data.Refused(ctx.T("calendar.subscribeurlneeded"))
			return ctx.Render(http.StatusUnprocessableEntity, "calendar/subscribe", data)
		}
		login := ctx.LoginFor(data.Account)
		if login == nil {
			return web.NewError(http.StatusBadRequest, "not signed in to that account")
		}
		sub := Subscription{URL: NormalizeFeedURL(data.Address), Color: dav.DefaultColor, Changed: time.Now().UTC()}
		rc := ctx.Request().Context()
		subs, err := LoadSubscriptions(rc, login.Store())
		if err != nil {
			return err
		}
		if slices.ContainsFunc(subs, func(s Subscription) bool { return s.URL == sub.URL }) {
			data.Refused(fmt.Sprintf(ctx.T("calendar.alreadysubscribed"), sub.URL))
			return ctx.Render(http.StatusUnprocessableEntity, "calendar/subscribe", data)
		}
		got, cal, err := p.Fetch(rc, sub.URL)
		if err != nil {
			data.Refused(fmt.Sprintf(ctx.T("calendar.feedfailed"), sub.URL, err))
			if errors.Is(err, ErrNotFeed) {
				data.Refused(fmt.Sprintf(ctx.T("calendar.notfeed"), sub.URL))
			}
			return ctx.Render(http.StatusUnprocessableEntity, "calendar/subscribe", data)
		}
		if sub.Name = FeedName(cal); sub.Name == "" {
			sub.Name = FeedHost(sub.URL)
		}
		if err := SaveSubscriptions(rc, login.Store(), append(subs, sub)); err != nil {
			data.Refused(ctx.SaveRefusal(err))
			return ctx.Render(http.StatusUnprocessableEntity, "calendar/subscribe", data)
		}
		// Filled with what was just fetched rather than asked again.
		if err := p.Fill(rc, login, sub.URL, got, cal); err != nil {
			return err
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.Path("calendar.month")))
	}
}
