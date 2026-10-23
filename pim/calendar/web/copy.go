package web

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

// copyTo writes an event, a task or a journal entry into a calendar the reader chooses, as an object of its own (ADR 63).
func (p *plugin) copyTo(s section) func(*web.Context) error {
	return p.transferTo(s, false)
}

func (p *plugin) moveTo(s section) func(*web.Context) error {
	return p.transferTo(s, true)
}

func (p *plugin) transferTo(s section, move bool) func(*web.Context) error {
	section, supports, key, first, rail, missing := "nav.calendar", calendar.SupportsEvent, "event", calendar.FirstEvent, p.eventRail, "notfound.event"
	switch s {
	case taskSection:
		section, supports, key, first, rail, missing = "nav.tasks", calendar.SupportsTodo, "task", calendar.FirstTodo, p.taskRail, "notfound.task"
	case journalSection:
		section, supports, key, first, rail, missing = "nav.journal", calendar.SupportsJournal, "entry", calendar.FirstJournal, p.journalRail, "notfound.entry"
	}
	return func(ctx *web.Context) error {
		src := ctx.Param("path")
		rc := ctx.Request().Context()
		c, calendars, err := p.Open(rc, ctx.Login)
		if err != nil {
			return err
		}
		held := dav.Holding(calendars, "", src)
		if held == nil {
			return alborz.NotFound(missing)
		}
		if move && (!held.Writable || held.Feed != "") {
			return web.NewError(http.StatusForbidden, ctx.T("form.readonlysource"))
		}
		co, err := calendar.GetObject(rc, c, src)
		if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
			return alborz.NotFound(missing)
		} else if err != nil {
			return err
		}
		comp := first(co.Data)
		if comp == nil {
			return alborz.NotFound(missing)
		}
		var kept url.Values
		if from := ctx.From(); from != "" {
			kept = url.Values{"from": {from}}
		}
		back := pimweb.ObjectURL(ctx, s.object, src, ctx.URLAccount(), kept)
		groups, err := p.writableGroups(ctx, supports)
		if err != nil {
			return err
		}

		page := func(status int, refusal string) error {
			r, err := rail(ctx)
			if err != nil {
				return err
			}
			summary, _ := comp.Props.Text(ical.PropSummary)
			title, label, save := "common.copyask", "common.saveto", "common.copydo"
			if move {
				title, label, save = "common.moveask", "common.moveto", "common.movedo"
			}
			data := destinationRenderData(ctx, r, ctx.T(section), ctx.Path(s.list), ctx.T(title), summary,
				ctx.T(label), ctx.T(save), key, groups, back)
			data.Refused(refusal)
			return ctx.Render(status, "calendar/destination", data)
		}
		if ctx.Request().Method != http.MethodPost {
			return page(http.StatusOK, "")
		}
		to, err := p.destination(ctx, ctx.FormValue("to"), supports)
		if handled, refusal := pimweb.DestinationRefusal(ctx, err, func(message string) error {
			return page(http.StatusUnprocessableEntity, message)
		}); handled {
			return refusal
		}
		if err != nil {
			return err
		}
		var made *caldav.CalendarObject
		if move {
			if to.Account == ctx.Login.Username() && to.Path == held.Path {
				return ctx.FoundRedirect(back)
			}
			made, err = p.moveObject(ctx, pim.Ref[*caldav.Client]{Client: c, Account: ctx.Login.Username(), Path: src}, to, co)
		} else {
			made, err = p.copyObject(ctx, pim.Ref[*caldav.Client]{Client: c, Account: ctx.Login.Username(), Path: src}, to, co, *held)
		}
		if err != nil {
			return page(http.StatusUnprocessableEntity, pimweb.Refusal(ctx, err))
		}

		name := to.Path
		for _, g := range groups {
			if g.Account == to.Account {
				if collection := dav.At(g.Collections, to.Path); collection != nil {
					name = collection.Name
				}
			}
		}
		sentence := ctx.T("notice.copiedto")
		notice := web.Notice{Kind: web.NoticeDone,
			Text:   fmt.Sprintf(sentence, name),
			Markup: template.HTML(fmt.Sprintf(sentence, web.NoticeName(name))),
			Actions: []*web.NoticeAction{ctx.Undo(ctx.Path(s.object+".delete", made.Path)+"?account="+web.QueryValue(to.Account),
				url.Values{"next": {back}})}}
		if move {
			sentence = ctx.T("notice.objectmovedto")
			notice.Text, notice.Markup, notice.Actions = fmt.Sprintf(sentence, name), template.HTML(fmt.Sprintf(sentence, web.NoticeName(name))), nil
		}
		ctx.Notify(notice)
		return ctx.FoundRedirect(pimweb.ObjectURL(ctx, s.object, made.Path, to.Account, kept))
	}
}

// copyObject keeps the complete calendar, giving its series a new identity and
// recording the source account's calendar rather than the destination's.
func (p *plugin) copyObject(ctx *web.Context, from, to pim.Ref[*caldav.Client], co *caldav.CalendarObject, held dav.Collection) (*caldav.CalendarObject, error) {
	rc := ctx.Request().Context()
	source := held.Feed
	if source == "" {
		var err error
		if source, err = p.DAV.Address(rc, ctx.LoginFor(from.Account), held.Path); err != nil {
			return nil, err
		}
	}
	uid := uuid.New().String()
	calendar.AsCopy(co.Data, uid, source, time.Now().UTC())
	return calendar.PutObject(rc, to, uid+".ics", nil, co.Data)
}

// copySelected offers one destination for the checked rows, including rows
// from other signed-in accounts and calendars the reader can only read.
func (p *plugin) copySelected(s section) func(*web.Context) error {
	section, supports, first, rail, route, key, missing := "nav.calendar", calendar.SupportsEvent, calendar.FirstEvent, p.eventRail, "calendar.events.copy", "event", "notfound.event"
	switch s {
	case taskSection:
		section, supports, first, rail, route, key, missing = "nav.tasks", calendar.SupportsTodo, calendar.FirstTodo, p.taskRail, "calendar.tasks.copy", "task", "notfound.task"
	case journalSection:
		section, supports, first, rail, route, key, missing = "nav.journal", calendar.SupportsJournal, calendar.FirstJournal, p.journalRail, "calendar.journal.copy", "entry", "notfound.entry"
	}
	return func(ctx *web.Context) error {
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		// Recurring occurrences can select the same stored object more than once.
		seen := make(map[string]bool)
		paths := make([]string, 0, len(params["paths"]))
		for _, ref := range params["paths"] {
			if !seen[ref] {
				seen[ref] = true
				paths = append(paths, ref)
			}
		}
		params["paths"] = paths
		fallback := ctx.From()
		if fallback == "" {
			fallback = ctx.URL(s.list)
		}
		next := ctx.NextOr(fallback)
		if len(params["paths"]) == 0 {
			return ctx.FoundRedirect(next)
		}
		// Validate account-qualified selections before showing or applying them.
		refs, err := pimweb.Selected(ctx, paths, p.Client)
		if err != nil {
			return err
		}
		page := func(status int, refusal string) error {
			groups, err := p.writableGroups(ctx, supports)
			if err != nil {
				return err
			}
			r, err := rail(ctx)
			if err != nil {
				return err
			}
			data := destinationRenderData(ctx, r, ctx.T(section), ctx.Path(s.list), ctx.T("common.copyask"),
				ctx.Tf("common.copycount", len(params["paths"])), ctx.T("common.saveto"), ctx.T("common.copydo"), key, groups, next)
			data.Action, data.Paths = ctx.Path(route), params["paths"]
			if from := ctx.From(); from != "" {
				data.Action += "?" + url.Values{"from": {from}}.Encode()
			}
			data.Refused(refusal)
			return ctx.Render(status, "calendar/destination", data)
		}
		if ctx.Request().Method != http.MethodPost {
			return page(http.StatusOK, "")
		}
		to, err := p.destination(ctx, params.Get("to"), supports)
		if handled, refusal := pimweb.DestinationRefusal(ctx, err, func(message string) error {
			return page(http.StatusUnprocessableEntity, message)
		}); handled {
			return refusal
		}
		if err != nil {
			return err
		}
		copyOne := func(ref pim.Ref[*caldav.Client]) error {
			rc := ctx.Request().Context()
			_, calendars, err := p.Open(rc, ctx.LoginFor(ref.Account))
			if err != nil {
				return err
			}
			held := dav.Holding(calendars, "", ref.Path)
			if held == nil {
				return alborz.NotFound(missing)
			}
			co, err := calendar.GetObject(rc, ref.Client, ref.Path)
			if err != nil {
				return err
			}
			if first(co.Data) == nil {
				return alborz.NotFound(missing)
			}
			_, err = p.copyObject(ctx, ref, to, co, *held)
			return err
		}
		for i, ref := range refs {
			if err := copyOne(ref); err != nil {
				// Retrying this form must not duplicate the copies already written.
				params["paths"] = paths[i:]
				refusal := pimweb.Refusal(ctx, err)
				if i > 0 {
					refusal = ctx.Tf("notice.itemscopied", i) + " " + refusal
				}
				return page(http.StatusUnprocessableEntity, refusal)
			}
		}
		ctx.Notify(web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.itemscopied", len(refs))})
		return ctx.FoundRedirect(next)
	}
}

// moveObject preserves the complete calendar and removes the source only while
// its ETag still matches. A refused delete rolls back only our destination version.
func (p *plugin) moveObject(ctx *web.Context, from, to pim.Ref[*caldav.Client], co *caldav.CalendarObject) (*caldav.CalendarObject, error) {
	rc := ctx.Request().Context()
	_, sources, err := p.Open(rc, ctx.LoginFor(from.Account))
	if err != nil {
		return nil, err
	}
	held := dav.Holding(sources, "", from.Path)
	if held == nil || !held.Writable || held.Feed != "" {
		return nil, webdav.NewHTTPError(http.StatusForbidden, errors.New(ctx.T("form.readonlysource")))
	}
	if from.Account == to.Account && held.Path == to.Path {
		return co, nil
	}
	if co.ETag == "" || dav.IfUnchanged(co.ETag) == "" {
		return nil, dav.ErrUnversionedObject
	}
	if from.Account == to.Account {
		_, destinations, err := p.Open(rc, ctx.LoginFor(to.Account))
		if err != nil {
			return nil, err
		}
		if dest := dav.At(destinations, to.Path); dest != nil && dest.Source == held.Source {
			at := to.Path + path.Base(from.Path)
			if handled, err := dav.MoveObject(rc, p.DAV.HTTPClient(rc, ctx.LoginFor(from.Account)), from.Path, at, co.ETag); handled {
				return &caldav.CalendarObject{Path: at}, err
			}
		}
	}
	// A scheduling server may send invitations on PUT or cancellation on DELETE.
	// Only native MOVE can preserve those objects there without scheduling them.
	for _, comp := range co.Data.Children {
		if comp.Name == ical.CompTimezone || (comp.Props.Get(ical.PropOrganizer) == nil && comp.Props.Get(ical.PropAttendee) == nil) {
			continue
		}
		for _, ref := range []pim.Ref[*caldav.Client]{from, {Account: to.Account, Path: to.Path + path.Base(from.Path)}} {
			schedules, err := p.ServerSchedules(rc, ctx.LoginFor(ref.Account), ref.Path)
			if err != nil || schedules {
				return nil, webdav.NewHTTPError(http.StatusConflict, errors.Join(errors.New(ctx.T("form.movescheduling")), err))
			}
		}
		break
	}
	made, err := calendar.PutObject(rc, to, path.Base(from.Path), nil, co.Data)
	if err != nil {
		return nil, err
	}
	if made.ETag == "" || dav.IfUnchanged(made.ETag) == "" {
		return nil, dav.ErrUnversionedDestination
	}
	if err := dav.RemoveObject(rc, p.DAV.HTTPClient(rc, ctx.LoginFor(from.Account)), from.Path, co.ETag); err != nil {
		if !dav.DeleteRefused(err) {
			return nil, errors.Join(dav.ErrMoveUncertain, err)
		}
		exists, checkErr := dav.SourceExists(rc, p.DAV.HTTPClient(rc, ctx.LoginFor(from.Account)), from.Path)
		if checkErr != nil || !exists {
			return nil, errors.Join(dav.ErrMoveUncertain, err, checkErr)
		}
		rollback := dav.RemoveObject(rc, p.DAV.HTTPClient(rc, ctx.LoginFor(to.Account)), made.Path, made.ETag)
		return nil, errors.Join(err, rollback)
	}
	return made, nil
}
