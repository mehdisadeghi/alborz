package calendar

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
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
)

// copyTo writes an event or a task into a calendar the reader chooses, as an object of its own (ADR 63).
func (p *plugin) copyTo(list string) func(*web.Context) error {
	section, supports, key, first, rail, missing := "nav.calendar", supportsEvent, "event", firstEvent, p.eventRail, "notfound.event"
	if list == "/tasks" {
		section, supports, key, first, rail, missing = "nav.tasks", supportsTodo, "task", getFirstTodo, p.taskRail, "notfound.task"
	}
	return func(ctx *web.Context) error {
		src, err := pim.ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		rc := ctx.Request().Context()
		c, calendars, err := p.clientWithCalendars(rc, ctx.Login)
		if err != nil {
			return err
		}
		held := dav.Holding(calendars, "", src)
		if held == nil {
			return alborz.NotFound(missing)
		}
		co, err := c.GetCalendarObject(rc, src)
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
		back := pim.ObjectURL(list+"/", src, ctx.URLAccount(), kept)
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
				Home:           list,
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
			if source, err = p.dav.Address(rc, ctx.Login, held.Path); err != nil {
				return err
			}
		}
		uid := uuid.New().String()
		asCopy(co.Data, uid, source, time.Now().UTC())
		made, err := putObject(ctx, to, uid+".ics", nil, co.Data)
		if err != nil {
			return page(http.StatusUnprocessableEntity, pim.Refusal(ctx, err))
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
			Actions: []*web.NoticeAction{ctx.Undo(list+"/"+url.PathEscape(made.Path)+"/delete?account="+web.QueryValue(to.Account),
				url.Values{"next": {back}})}})
		return ctx.Redirect(http.StatusFound, pim.ObjectURL(list+"/", made.Path, to.Account, kept))
	}
}

// asCopy makes cal an object of its own: a fresh UID, no invitation, and a link to where it came from.
// RFC 9253's LINK with the registered relation "via" names a source; RELATED-TO relates components of
// one calendar, and without a RELTYPE, of which none means a copy, makes the copy the original's child.
func asCopy(cal *ical.Calendar, uid, source string, now time.Time) {
	cal.Props.Del(ical.PropMethod)
	for _, comp := range cal.Children {
		if comp.Name == ical.CompTimezone {
			continue
		}
		was, _ := comp.Props.Text(ical.PropUID)
		comp.Props.SetText(ical.PropUID, uid)
		comp.Props.SetDateTime(ical.PropDateTimeStamp, now)
		comp.Props.Del(ical.PropOrganizer)
		comp.Props.Del(ical.PropAttendee)
		comp.Props.Add(link("URI", source))
		comp.Props.Add(link("UID", was))
	}
}

// The names RFC 9253 gives LINK and its relation parameter.
const (
	propLink     = "LINK"
	paramLinkRel = "LINKREL"
)

func link(kind, value string) *ical.Prop {
	prop := ical.NewProp(propLink)
	prop.Params.Set(ical.ParamValue, kind)
	prop.Params.Set(paramLinkRel, "via")
	prop.Value = value
	return prop
}
