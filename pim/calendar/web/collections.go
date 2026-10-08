package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
)

func (p *plugin) invitations(ctx *web.Context) (*pimweb.RailInvitations, error) {
	return pimweb.Invitations(ctx, p.DAV, p.collectionPage().Collection)
}

func (p *plugin) collectionPage() pimweb.Page {
	return pimweb.Page{
		Collection: "calendar.collection",
		List:       "calendar.month",
		Ext:        ".ics",
		Forget:     p.DAV.Forget,
		Show:       calendar.Show,
		Rail: func(ctx *web.Context, list string) (pimweb.Rail, error) {
			switch list {
			case ctx.Path("calendar.tasks"):
				return p.taskRail(ctx)
			case ctx.Path("calendar.journal"):
				return p.journalRail(ctx)
			}
			return p.eventRail(ctx)
		},
		Import: func(ctx *web.Context, path string, raw []byte) (int, error) {
			c, _, err := p.Open(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return 0, err
			}
			return calendar.Import(ctx.Request().Context(), c, path, raw)
		},
		Create: func(ctx *web.Context, list, name, place string) (string, error) {
			// A task list takes tasks, a journal entries; an imported calendar keeps events and tasks both.
			components := []string{"VEVENT", "VTODO"}
			switch list {
			case ctx.Path("calendar.tasks"):
				components = []string{"VTODO"}
			case ctx.Path("calendar.journal"):
				components = []string{"VJOURNAL"}
			}
			return p.DAV.Create(ctx.Request().Context(), ctx.Login, name, dav.DefaultColor, place, components)
		},
		Export: func(ctx *web.Context, path string, from, to time.Time) ([]byte, error) {
			c, _, err := p.Open(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return nil, err
			}
			return calendar.Export(ctx.Request().Context(), c, path, from, to)
		},
		Lookup: func(ctx *web.Context, path string) (dav.Collection, func() int, string, string, error) {
			return pimweb.LookupCollection(ctx, path, p.Open, p.DAV.CountObjects, func(info dav.Collection) (string, string) {
				list := collectionList(ctx, info)
				label := ctx.T("nav.calendar")
				switch list {
				case ctx.Path("calendar.tasks"):
					label = ctx.T("nav.tasks")
				case ctx.Path("calendar.journal"):
					label = ctx.T("nav.journal")
				}
				return list, label
			})
		},
	}
}

// collectionList is the first of the calendar, tasks and journal that the collection holds.
func collectionList(ctx *web.Context, info dav.Collection) string {
	switch {
	case calendar.SupportsEvent(info.Components):
		return ctx.Path("calendar.month")
	case calendar.SupportsTodo(info.Components):
		return ctx.Path("calendar.tasks")
	case calendar.SupportsJournal(info.Components):
		return ctx.Path("calendar.journal")
	}
	return ctx.Path("calendar.tasks")
}

func (p *plugin) createForm(ctx *web.Context) (pimweb.CreateForm, error) {
	// Back to the rail that asked, when the new collection shows there.
	made := func(holds string) (string, string) {
		switch holds {
		case "tasks":
			return ctx.T("notice.tasklistcreated"), ctx.Path("calendar.tasks")
		case "journal":
			return ctx.T("notice.journalcreated"), ctx.Path("calendar.journal")
		}
		return ctx.T("notice.calendarcreated"), ctx.Path("calendar.month")
	}
	switch ctx.QueryParam("for") {
	case "tasks":
		rail, err := p.taskRail(ctx)
		return pimweb.CreateForm{Rail: rail, Title: ctx.T("tasks.newlist"), Section: ctx.T("nav.tasks"), List: ctx.Path("calendar.tasks"),
			Holds: "tasks", Made: made}, err
	case "journal":
		rail, err := p.journalRail(ctx)
		return pimweb.CreateForm{Rail: rail, Title: ctx.T("journal.newjournal"), Section: ctx.T("nav.journal"), List: ctx.Path("calendar.journal"),
			Holds: "journal", Made: made}, err
	}
	rail, err := p.eventRail(ctx)
	return pimweb.CreateForm{Rail: rail, Title: ctx.T("calendar.newcalendar"), Section: ctx.T("nav.calendar"), List: ctx.Path("calendar.month"),
		Holds: "both", OffersHolds: true, Made: made}, err
}

// SubscribeRenderData renders subscribe-calendar.html.
type SubscribeRenderData struct {
	web.PageData[pimweb.Rail]
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
			PageData: *web.NewPageData(ctx, ctx.T("calendar.subscribe"), rail),
			Accounts: ctx.Accounts(),
			Account:  ctx.Login.Username(),
			Next:     ctx.FormValue("next"),
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
		sub := calendar.Subscription{URL: calendar.NormalizeFeedURL(data.Address), Color: dav.DefaultColor, Changed: time.Now().UTC()}
		rc := ctx.Request().Context()
		subs, err := calendar.LoadSubscriptions(rc, login.Store())
		if err != nil {
			return err
		}
		if slices.ContainsFunc(subs, func(s calendar.Subscription) bool { return s.URL == sub.URL }) {
			data.Refused(fmt.Sprintf(ctx.T("calendar.alreadysubscribed"), sub.URL))
			return ctx.Render(http.StatusUnprocessableEntity, "calendar/subscribe", data)
		}
		got, cal, err := p.Fetch(rc, sub.URL)
		if err != nil {
			data.Refused(fmt.Sprintf(ctx.T("calendar.feedfailed"), sub.URL, err))
			if errors.Is(err, calendar.ErrNotFeed) {
				data.Refused(fmt.Sprintf(ctx.T("calendar.notfeed"), sub.URL))
			}
			return ctx.Render(http.StatusUnprocessableEntity, "calendar/subscribe", data)
		}
		if sub.Name = calendar.FeedName(cal); sub.Name == "" {
			sub.Name = calendar.FeedHost(sub.URL)
		}
		added, err := calendar.Subscribe(rc, login, sub)
		if err != nil {
			data.Refused(ctx.SaveRefusal(err))
			return ctx.Render(http.StatusUnprocessableEntity, "calendar/subscribe", data)
		}
		if !added {
			data.Refused(fmt.Sprintf(ctx.T("calendar.alreadysubscribed"), sub.URL))
			return ctx.Render(http.StatusUnprocessableEntity, "calendar/subscribe", data)
		}
		// Filled with what was just fetched rather than asked again.
		if err := p.Fill(rc, login, sub.URL, got, cal); err != nil {
			return err
		}
		return ctx.NotifyRedirect(web.Notice{Kind: web.NoticeDone, Text: fmt.Sprintf(ctx.T("notice.subscribed"), sub.Name)}, ctx.NextOr(ctx.Path("calendar.month")))
	}
}
