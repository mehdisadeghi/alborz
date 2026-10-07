package web

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/dates"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

type EntryRenderData struct {
	web.BaseRenderData
	Rail     pimweb.Rail
	Calendar *dav.Collection
	Path     string
	Journal  *ical.Component
	Summary  string
	Text     string
	// Date is DTSTART; Timed says it names a time of day as well.
	Date  time.Time
	Timed bool
	// Status is DRAFT, FINAL or CANCELLED in lower case, empty for none or another value.
	Status     string
	Categories []string
	Star       string
	// Authors are who added and last changed the object, where another account.
	Authors dav.Authors
	// List is the list the page was opened from.
	List       string
	Neighbours web.Neighbours
}

type UpdateEntryRenderData struct {
	web.BaseRenderData
	Rail           pimweb.Rail
	Groups         []dav.Group
	Calendar       *dav.Collection
	CalendarObject *caldav.CalendarObject
	Summary, Text  string
	// Date is the entry's day as the field holds it.
	Date dates.Field
}

// RFC 5545 3.8.1.11's statuses of a VJOURNAL.
var entryStatuses = []string{"draft", "final", "cancelled"}

func (p *plugin) entry(ctx *web.Context) error {
	path := ctx.Param("path")
	c, calendars, err := p.Open(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	coll := dav.Holding(calendars, "", path)
	if coll == nil {
		return alborz.NotFound("notfound.entry")
	}
	obj, err := calendar.GetObject(ctx.Request().Context(), c, path)
	if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
		return alborz.NotFound("notfound.entry")
	} else if err != nil {
		return err
	}
	journal := calendar.FirstJournal(obj.Data)
	if journal == nil {
		return alborz.NotFound("notfound.entry")
	}
	summary, _ := journal.Props.Text(ical.PropSummary)
	date, err := journal.Props.DateTime(ical.PropDateTimeStart, web.UserLocation(ctx))
	if err != nil {
		return fmt.Errorf("%w: %s: %v", alborz.ErrUnreadable, path, err)
	}
	status, _ := journal.Props.Text(ical.PropStatus)
	status = strings.ToLower(status)
	if !slices.Contains(entryStatuses, status) {
		status = ""
	}
	start := journal.Props.Get(ical.PropDateTimeStart)

	rail, err := p.journalRail(ctx)
	if err != nil {
		return err
	}
	list, err := p.journalList(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "journal/entry", &EntryRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(summary).WithItem(),
		Rail:           rail,
		Calendar:       coll,
		Path:           obj.Path,
		Journal:        journal,
		Summary:        summary,
		Text:           calendar.JournalText(journal),
		Date:           date,
		Timed:          start != nil && start.ValueType() != ical.ValueDate,
		Status:         status,
		Categories:     calendar.Categories(journal),
		Star:           calendar.ComponentColor(journal),
		Authors:        p.DAV.Authors(obj.Path, ctx.Login.Username()),
		List:           cmp.Or(ctx.From(), pimweb.ListURL(ctx.Path("calendar.journal"), pimweb.ListParams(ctx, journalListParams...))),
		Neighbours:     pimweb.Around(list.Items, path),
	})
}

func (p *plugin) updateEntry(ctx *web.Context) error {
	entryPath := ctx.Param("path")
	loc := web.UserLocation(ctx)

	var c *caldav.Client
	var groups []dav.Group
	var co *caldav.CalendarObject
	var journal *ical.Component
	current := &dav.Collection{}
	if entryPath != "" {
		var calendars []dav.Collection
		var err error
		if c, calendars, err = p.Open(ctx.Request().Context(), ctx.Login); err != nil {
			return err
		}
		co, err = calendar.GetObject(ctx.Request().Context(), c, entryPath)
		if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
			return alborz.NotFound("notfound.entry")
		} else if err != nil {
			return err
		}
		if journal = calendar.FirstJournal(co.Data); journal == nil {
			return alborz.NotFound("notfound.entry")
		}
		current = dav.Holding(calendars, "", co.Path)
	} else {
		var err error
		if groups, err = p.writableGroups(ctx, calendar.SupportsJournal); err != nil {
			return err
		}
		journal = ical.NewComponent(ical.CompJournal)
		journal.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
		journal.Props.SetDate(ical.PropDateTimeStart, time.Now().In(loc))
		if len(groups) > 0 {
			current = &groups[0].Collections[0]
		}
	}
	date, err := journal.Props.DateTime(ical.PropDateTimeStart, loc)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", alborz.ErrUnreadable, entryPath, err)
	}
	summary, _ := journal.Props.Text(ical.PropSummary)
	data := &UpdateEntryRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("journal.createtitle")),
		Groups:         groups,
		Calendar:       current,
		CalendarObject: co,
		Summary:        summary,
		Text:           calendar.JournalText(journal),
		Date:           ctx.Dates().Input(date),
	}
	if co != nil {
		data.WithTitle(fmt.Sprintf(ctx.T("title.update"), summary))
	}
	render := func(status int) error {
		rail, err := p.journalRail(ctx)
		if err != nil {
			return err
		}
		data.Rail = rail
		return ctx.Render(status, "journal/entry-edit", data)
	}
	if ctx.Request().Method != http.MethodPost {
		return render(http.StatusOK)
	}

	params, err := ctx.FormParams()
	if err != nil {
		return err
	}
	if co != nil {
		if co.ETag, err = pimweb.Drawn(ctx, co.ETag); err != nil {
			return err
		}
	}
	// A form without a field changes nothing of it; one sent empty removes it.
	_, dateSent := params["date"]
	_, textSent := params["description"]
	data.Summary = ctx.FormValue("summary")
	if textSent {
		data.Text = strings.ReplaceAll(ctx.FormValue("description"), "\r", "")
	}
	if dateSent {
		data.Date = ctx.DateField("date")
	}
	reject := func(message string) error {
		data.Refused(message)
		return render(http.StatusUnprocessableEntity)
	}
	if data.Summary == "" {
		return reject(ctx.T("form.summaryneeded"))
	}
	to := pim.Ref[*caldav.Client]{Client: c}
	creating := co == nil
	if creating {
		to, err = p.destination(ctx, ctx.FormValue("calendar"), calendar.SupportsJournal)
		if errors.Is(err, pim.ErrNoDestination) {
			return reject(ctx.T("form.destinationneeded"))
		} else if err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	journal.Props.SetDateTime(ical.PropDateTimeStamp, now)
	journal.Props.SetDateTime(ical.PropLastModified, now)
	journal.Props.SetText(ical.PropSummary, data.Summary)
	switch {
	case !textSent:
	case data.Text != "":
		journal.Props.SetText(ical.PropDescription, data.Text)
	default:
		journal.Props.Del(ical.PropDescription)
	}
	switch {
	case !dateSent:
	case data.Date.Value == "":
		journal.Props.Del(ical.PropDateTimeStart)
	default:
		day, err := data.Date.Read(loc)
		if err != nil {
			return reject(ctx.T("form.notdate"))
		}
		// A time another client wrote stays while the day does.
		if y, m, d := day.Date(); date.IsZero() || date.Year() != y || date.Month() != m || date.Day() != d {
			journal.Props.SetDate(ical.PropDateTimeStart, day)
		}
	}
	uid := uuid.New().String()
	if journal.Props.Get(ical.PropUID) == nil {
		journal.Props.SetText(ical.PropUID, uid)
	}
	cal := calendar.NewCalendar(journal)
	if !creating {
		cal = co.Data
	}
	co, err = calendar.PutObject(ctx.Request().Context(), to, uid+".ics", co, cal)
	if err != nil {
		ctx.Notify(pimweb.Unsaved(ctx, err))
		return reject("")
	}
	return pimweb.Saved(ctx, creating, ctx.T("notice.entrycreated"), data.Summary, ctx.Path("calendar.entry", co.Path), ctx.Path("calendar.journal"), to.Account)
}
