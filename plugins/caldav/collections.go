package alborzcaldav

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/labstack/echo/v4"
)

// collectionPage is a calendar's own page: the list it belongs to is
// the tasks rail when it holds no events.
func (p *plugin) collectionPage() dav.Page {
	return dav.Page{
		Base:   "/calendars/",
		List:   "/calendar",
		Color:  calendarColor,
		Ext:    ".ics",
		Forget: p.dav.Forget,
		Show:   show,
		Rail: func(ctx *alborz.Context, list string) (dav.Rail, error) {
			if list == "/tasks" {
				return p.taskRail(ctx)
			}
			return p.eventRail(ctx)
		},
		Import: func(ctx *alborz.Context, path string, raw []byte) (int, error) {
			c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return 0, err
			}
			return importObjects(ctx.Request().Context(), c, path, raw)
		},
		Create: func(ctx *alborz.Context, list, name, place string) (string, error) {
			// A task list takes tasks; a calendar brought in whole keeps
			// what it held, events and tasks both.
			components := []string{"VEVENT", "VTODO"}
			if list == "/tasks" {
				components = []string{"VTODO"}
			}
			return p.dav.Create(ctx.Request().Context(), ctx.Session, name, dav.DefaultColor, place, components)
		},
		Export: func(ctx *alborz.Context, path string, from, to time.Time) ([]byte, error) {
			c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return nil, err
			}
			return exportCalendar(ctx.Request().Context(), c, path, from, to)
		},
		Lookup: func(ctx *alborz.Context, path string) (dav.Collection, func() int, string, string, error) {
			if isSubscription(path) {
				settings, err := loadSettings(ctx.Session.Store())
				if err != nil {
					return dav.Collection{}, nil, "", "", err
				}
				i := subscriptionAt(settings.Subscriptions, path)
				if i < 0 {
					return dav.Collection{}, nil, "", "", alborz.NotFound("notfound.collection")
				}
				sub := settings.Subscriptions[i]
				return sub.info(ctx.Session.Username()),
					func() int { return subs.count(sub.URL) }, "/calendar", ctx.T("nav.calendar"), nil
			}
			_, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
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

// forSubscription routes a collection page's POST to the subscription
// handler when the path names one; everything else goes to the server.
func (p *plugin) forSubscription(sub func(*alborz.Context, string) error, next func(*alborz.Context) error) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		path, err := dav.ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		path = dav.CanonicalCollectionPath(path)
		if ctx.Request().Method != http.MethodPost || !isSubscription(path) {
			return next(ctx)
		}
		return sub(ctx, path)
	}
}

// updateSubscription saves the name and colour the page was given.
func (p *plugin) updateSubscription(ctx *alborz.Context, path string) error {
	settings, err := loadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	i := subscriptionAt(settings.Subscriptions, path)
	if i < 0 {
		return alborz.NotFound("notfound.collection")
	}
	name := strings.TrimSpace(ctx.FormValue("name"))
	if name == "" {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, ctx.T("form.nameneeded"))
	}
	settings.Subscriptions[i].Name = name
	settings.Subscriptions[i].Color = ctx.FormValue("color")
	if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
		return err
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath("/calendar")))
}

// unsubscribe drops the feed; nothing is deleted anywhere.
func (p *plugin) unsubscribe(ctx *alborz.Context, path string) error {
	settings, err := loadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	i := subscriptionAt(settings.Subscriptions, path)
	if i < 0 {
		return alborz.NotFound("notfound.collection")
	}
	name := settings.Subscriptions[i].Name
	settings.Subscriptions = append(settings.Subscriptions[:i], settings.Subscriptions[i+1:]...)
	if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
		return err
	}
	ctx.PutNotice(fmt.Sprintf(ctx.T("notice.unsubscribedcalendar"), name))
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/calendar"))
}

// collectionList is the page a collection belongs to.
func collectionList(info dav.Collection) string {
	if !supportsEvent(info.Components) {
		return "/tasks"
	}
	return "/calendar"
}

// createForm is the form for a new calendar. A task list is a calendar
// whose component set holds VTODO; RFC 4791 knows no other kind of
// collection. One form makes both, entered from whichever rail wants
// one, and the tasks entrance offers no choice of what it holds.
func (p *plugin) createForm(ctx *alborz.Context) (dav.CreateForm, error) {
	forTasks := ctx.QueryParam("for") == "tasks"
	// Back to the rail it was asked for, when the new collection shows
	// there; a task list never appears under calendars.
	made := func(holds string) (string, string) {
		if holds == "tasks" {
			return ctx.T("notice.tasklistcreated"), "/tasks"
		}
		return ctx.T("notice.calendarcreated"), "/calendar"
	}
	if forTasks {
		rail, err := p.taskRail(ctx)
		return dav.CreateForm{Rail: rail, Title: ctx.T("tasks.newlist"), Section: ctx.T("nav.tasks"), List: "/tasks",
			Holds: "tasks", Made: made}, err
	}
	rail, err := p.eventRail(ctx)
	return dav.CreateForm{Rail: rail, Title: ctx.T("calendar.newcalendar"), Section: ctx.T("nav.calendar"), List: "/calendar",
		Holds: "both", OffersHolds: true, Made: made}, err
}

// SubscribeRenderData renders subscribe-calendar.html.
type SubscribeRenderData struct {
	alborz.BaseRenderData
	Rail     dav.Rail
	Accounts []alborz.Account
	Account  string
	Address  string
	Next     string
	Error    string
}

// handleSubscribe follows a calendar by its address. The feed is fetched
// once now, so a bad address is refused here and a good one shows on
// the next view without waiting for the poll; the feed names itself,
// and the colour is changed on the calendar's own page.
func handleSubscribe(p *plugin) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		rail, err := p.eventRail(ctx)
		if err != nil {
			return err
		}
		data := &SubscribeRenderData{
			Rail:           rail,
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("calendar.subscribe")),
			Accounts:       ctx.Accounts(),
			Account:        ctx.Session.Username(),
			Next:           ctx.FormValue("next"),
		}
		if ctx.Request().Method != http.MethodPost {
			return ctx.Render(http.StatusOK, "subscribe-calendar.html", data)
		}
		data.Address = strings.TrimSpace(ctx.FormValue("address"))
		if account := ctx.FormValue("account"); account != "" {
			data.Account = account
		}
		if data.Address == "" {
			data.Error = ctx.T("calendar.subscribeurlneeded")
			return ctx.Render(http.StatusUnprocessableEntity, "subscribe-calendar.html", data)
		}
		session := ctx.SessionFor(data.Account)
		if session == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
		}
		sub := Subscription{URL: normalizeFeedURL(data.Address), Color: dav.DefaultColor}
		settings, err := loadSettings(session.Store())
		if err != nil {
			return err
		}
		if subscriptionAt(settings.Subscriptions, sub.path()) >= 0 {
			data.Error = fmt.Sprintf(ctx.T("calendar.alreadysubscribed"), sub.URL)
			return ctx.Render(http.StatusUnprocessableEntity, "subscribe-calendar.html", data)
		}
		if err := subs.refresh(sub.URL); err != nil {
			data.Error = fmt.Sprintf(ctx.T("calendar.feedfailed"), sub.URL, err)
			if errors.Is(err, errNotFeed) {
				data.Error = fmt.Sprintf(ctx.T("calendar.notfeed"), sub.URL)
			}
			return ctx.Render(http.StatusUnprocessableEntity, "subscribe-calendar.html", data)
		}
		sub.Name = subs.name(sub.URL)
		if sub.Name == "" {
			sub.Name = feedHost(sub.URL)
		}
		settings.Subscriptions = append(settings.Subscriptions, sub)
		if err := session.Store().Put(settingsKey, settings); err != nil {
			return err
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr("/calendar"))
	}
}
