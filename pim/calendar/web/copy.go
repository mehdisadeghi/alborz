package web

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
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
)

// copyTo writes an event or a task into a calendar the reader chooses, as an object of its own (ADR 63).
func (p *plugin) copyTo(s section) func(*web.Context) error {
	section, supports, key, first, rail, missing := "nav.calendar", calendar.SupportsEvent, "event", calendar.FirstEvent, p.eventRail, "notfound.event"
	if s == taskSection {
		section, supports, key, first, rail, missing = "nav.tasks", calendar.SupportsTodo, "task", calendar.FirstTodo, p.taskRail, "notfound.task"
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
			data := &DestinationRenderData{
				BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("common.copyask")),
				Rail:           r,
				Section:        ctx.T(section),
				Home:           ctx.Path(s.list),
				Title:          ctx.T("common.copyask"),
				Lead:           summary,
				Label:          ctx.T("common.saveto"),
				Save:           ctx.T("common.copydo"),
				Key:            key,
				Groups:         groups,
				Next:           back,
			}
			data.Refused(refusal)
			return ctx.Render(status, "calendar/destination", data)
		}
		if ctx.Request().Method != http.MethodPost {
			return page(http.StatusOK, "")
		}
		to, err := p.destination(ctx, ctx.FormValue("to"), supports)
		if errors.Is(err, pim.ErrNoDestination) {
			return page(http.StatusUnprocessableEntity, ctx.T("form.destinationneeded"))
		} else if err != nil {
			return err
		}
		source := held.Feed
		if source == "" {
			if source, err = p.DAV.Address(rc, ctx.Login, held.Path); err != nil {
				return err
			}
		}
		uid := uuid.New().String()
		calendar.AsCopy(co.Data, uid, source, time.Now().UTC())
		made, err := calendar.PutObject(ctx.Request().Context(), to, uid+".ics", nil, co.Data)
		if err != nil {
			return page(http.StatusUnprocessableEntity, pimweb.Refusal(ctx, err))
		}

		name := ""
		for _, g := range groups {
			if g.Account == to.Account {
				name = dav.At(g.Collections, to.Path).Name
			}
		}
		sentence := ctx.T("notice.copiedto")
		ctx.Notify(web.Notice{Kind: web.NoticeDone,
			Text:   fmt.Sprintf(sentence, name),
			Markup: template.HTML(fmt.Sprintf(sentence, web.NoticeName(name))),
			Actions: []*web.NoticeAction{ctx.Undo(ctx.Path(s.object+".delete", made.Path)+"?account="+web.QueryValue(to.Account),
				url.Values{"next": {back}})}})
		return ctx.Redirect(http.StatusFound, pimweb.ObjectURL(ctx, s.object, made.Path, to.Account, kept))
	}
}
