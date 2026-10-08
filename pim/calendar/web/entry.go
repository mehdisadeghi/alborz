package web

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/dates"
	"git.mehdix.org/alborz/pim/calendar"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
)

func setDescription(component *ical.Component, description string, sent bool) {
	if !sent {
		return
	}
	if description == "" {
		component.Props.Del(ical.PropDescription)
		return
	}
	component.Props.SetText(ical.PropDescription, strings.ReplaceAll(description, "\r", ""))
}

type EntryRenderData struct {
	CalendarObjectRenderData
	Path    string
	Journal *ical.Component
	Summary string
	Text    string
	// Date is DTSTART; Timed says it names a time of day as well.
	Date  time.Time
	Timed bool
	// Status is DRAFT, FINAL or CANCELLED in lower case, empty for none or another value.
	Status     string
	Categories []string
	Star       string
	// Authors are who added and last changed the object, where another account.
	// List is the list the page was opened from.
}

type UpdateEntryRenderData struct {
	UpdateCalendarObjectRenderData
	Summary, Text string
	// Date is the entry's day as the field holds it.
	Date dates.Field
}

// RFC 5545 3.8.1.11's statuses of a VJOURNAL.
var entryStatuses = []string{"draft", "final", "cancelled"}

func (p *plugin) entry(ctx *web.Context) error {
	path := ctx.Param("path")
	e, err := p.openCalendarObject(ctx, path, "notfound.entry", "failed to get journal entry", alborz.NotFound("notfound.entry"), calendar.FirstJournal)
	if err != nil {
		return err
	}
	obj, journal := e.object, e.component
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
		PageData:   *web.NewPageData(ctx, summary, rail).WithItem(),
		Collection: e.collection,
		Path:       obj.Path,
		Journal:    journal,
		Summary:    summary,
		Text:       calendar.JournalText(journal),
		Date:       date,
		Timed:      start != nil && start.ValueType() != ical.ValueDate,
		Status:     status,
		Categories: calendar.Categories(journal),
		Star:       calendar.ComponentColor(journal),
		Authors:    p.DAV.Authors(obj.Path, ctx.Login.Username()),
		List:       cmp.Or(ctx.From(), pimweb.ListURL(ctx.Path("calendar.journal"), pimweb.ListParams(ctx, journalListParams...))),
		Neighbours: pimweb.Around(list.Items, path),
	})
}

func (p *plugin) updateEntry(ctx *web.Context) error {
	entryPath := ctx.Param("path")
	loc := web.UserLocation(ctx)
	e, err := p.editCalendarObject(ctx, entryPath, "notfound.entry", "failed to get journal entry", alborz.NotFound("notfound.entry"), calendar.SupportsJournal,
		calendar.FirstJournal, func() *ical.Component {
			journal := ical.NewComponent(ical.CompJournal)
			journal.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
			journal.Props.SetDate(ical.PropDateTimeStart, time.Now().In(loc))
			return journal
		})
	if err != nil {
		return err
	}
	journal := e.component
	date, err := journal.Props.DateTime(ical.PropDateTimeStart, loc)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", alborz.ErrUnreadable, entryPath, err)
	}
	summary, _ := journal.Props.Text(ical.PropSummary)
	data := &UpdateEntryRenderData{
		PageData:   *web.NewPageData(ctx, ctx.T("journal.createtitle"), pimweb.Rail{}),
		Groups:     e.groups,
		Collection: e.collection,
		Object:     e.object,
		Summary:    summary,
		Text:       calendar.JournalText(journal),
		Date:       ctx.Dates().Input(date),
	}
	if !e.creating() {
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
	if e.object != nil {
		if e.object.ETag, err = pimweb.Drawn(ctx, e.object.ETag); err != nil {
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
	to, err := e.destination(ctx, p, calendar.SupportsJournal)
	if handled, refusal := pimweb.DestinationRefusal(ctx, err, reject); handled {
		return refusal
	}
	if err != nil {
		return err
	}
	creating := e.creating()

	journal.Props.SetText(ical.PropSummary, data.Summary)
	setDescription(journal, data.Text, textSent)
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
	_, err = e.put(ctx, to, journal, time.Time{})
	if err != nil {
		ctx.Notify(pimweb.Unsaved(ctx, err))
		return reject("")
	}
	return pimweb.Saved(ctx, creating, ctx.T("notice.entrycreated"), data.Summary, ctx.Path("calendar.entry", e.object.Path), ctx.Path("calendar.journal"), to.Account)
}
