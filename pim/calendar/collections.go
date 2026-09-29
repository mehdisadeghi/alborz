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
	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/web"
	"github.com/labstack/echo/v4"
)

func (p *plugin) invitations(ctx *web.Context) (*dav.RailInvitations, error) {
	return p.dav.RailInvitations(ctx, p.collectionPage().Base)
}

func (p *plugin) collectionPage() pim.Page {
	return pim.Page{
		Base:   "/calendars/",
		List:   "/calendar",
		Ext:    ".ics",
		Forget: p.dav.Forget,
		Show:   show,
		Rail: func(ctx *web.Context, list string) (pim.Rail, error) {
			if list == "/tasks" {
				return p.taskRail(ctx)
			}
			return p.eventRail(ctx)
		},
		Import: func(ctx *web.Context, path string, raw []byte) (int, error) {
			c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return 0, err
			}
			return importObjects(ctx.Request().Context(), c, path, raw)
		},
		Create: func(ctx *web.Context, list, name, place string) (string, error) {
			// A task list takes tasks; an imported calendar keeps events and tasks both.
			components := []string{"VEVENT", "VTODO"}
			if list == "/tasks" {
				components = []string{"VTODO"}
			}
			return p.dav.Create(ctx.Request().Context(), ctx.Login, name, dav.DefaultColor, place, components)
		},
		Export: func(ctx *web.Context, path string, from, to time.Time) ([]byte, error) {
			c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return nil, err
			}
			return exportCalendar(ctx.Request().Context(), c, path, from, to)
		},
		Lookup: func(ctx *web.Context, path string) (dav.Collection, func() int, string, string, error) {
			_, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return dav.Collection{}, nil, "", "", err
			}
			info := dav.At(calendars, path)
			if info == nil {
				return dav.Collection{}, nil, "", "", alborz.NotFound("notfound.collection")
			}
			list := collectionList(*info)
			label := ctx.T("nav.calendar")
			if list == "/tasks" {
				label = ctx.T("nav.tasks")
			}
			return *info, p.dav.CountObjects(ctx, info.Path), list, label, nil
		},
	}
}

func collectionList(info dav.Collection) string {
	if !supportsEvent(info.Components) {
		return "/tasks"
	}
	return "/calendar"
}

func (p *plugin) createForm(ctx *web.Context) (pim.CreateForm, error) {
	forTasks := ctx.QueryParam("for") == "tasks"
	// Back to the rail that asked, when the new collection shows there.
	made := func(holds string) (string, string) {
		if holds == "tasks" {
			return ctx.T("notice.tasklistcreated"), "/tasks"
		}
		return ctx.T("notice.calendarcreated"), "/calendar"
	}
	if forTasks {
		rail, err := p.taskRail(ctx)
		return pim.CreateForm{Rail: rail, Title: ctx.T("tasks.newlist"), Section: ctx.T("nav.tasks"), List: "/tasks",
			Holds: "tasks", Made: made}, err
	}
	rail, err := p.eventRail(ctx)
	return pim.CreateForm{Rail: rail, Title: ctx.T("calendar.newcalendar"), Section: ctx.T("nav.calendar"), List: "/calendar",
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
			return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
		}
		sub := Subscription{URL: normalizeFeedURL(data.Address), Color: dav.DefaultColor, Changed: time.Now().UTC()}
		rc := ctx.Request().Context()
		subs, err := loadSubscriptions(rc, login.Store())
		if err != nil {
			return err
		}
		if slices.ContainsFunc(subs, func(s Subscription) bool { return s.URL == sub.URL }) {
			data.Refused(fmt.Sprintf(ctx.T("calendar.alreadysubscribed"), sub.URL))
			return ctx.Render(http.StatusUnprocessableEntity, "calendar/subscribe", data)
		}
		got, cal, err := fetchFeed(rc, p.feeds.client, store.Feed{URL: sub.URL})
		if err != nil {
			data.Refused(fmt.Sprintf(ctx.T("calendar.feedfailed"), sub.URL, err))
			if errors.Is(err, errNotFeed) {
				data.Refused(fmt.Sprintf(ctx.T("calendar.notfeed"), sub.URL))
			}
			return ctx.Render(http.StatusUnprocessableEntity, "calendar/subscribe", data)
		}
		if sub.Name = feedName(cal); sub.Name == "" {
			sub.Name = feedHost(sub.URL)
		}
		if err := login.Store().Put(rc, subscriptionsKey, append(subs, sub)); err != nil {
			data.Refused(ctx.SaveRefusal(err))
			return ctx.Render(http.StatusUnprocessableEntity, "calendar/subscribe", data)
		}
		// Filled with what was just fetched rather than asked again.
		objects, err := p.feeds.objects(sub.URL, cal)
		if err != nil {
			return err
		}
		if err := p.follows(rc, login, func(c store.Collection) error { return p.feeds.store.Fill(c.Ref(), got, objects) }); err != nil {
			return err
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr("/calendar"))
	}
}
