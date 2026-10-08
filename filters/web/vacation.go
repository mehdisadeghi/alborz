package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/filters"
	"git.mehdix.org/alborz/internal/dates"
	"git.mehdix.org/alborz/internal/locale"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
)

type RepliesRenderData struct {
	sievePageData
	Replies []filters.Reply
	// Active is the index of the reply the script sends, -1 when none.
	Active  int
	Editing filters.Reply
	Index   int
	// Shown is the reply the form was drawn from, which a save compares the form against.
	Shown      string
	Identities []string
	CanReply   bool
	CanDate    bool

	// From and Until are Editing's date fields as shown.
	From, Until dates.Field
}

func repliesData(ctx *web.Context) (*RepliesRenderData, error) {
	replies, err := filters.LoadReplies(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return nil, err
	}
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return nil, err
	}
	data := &RepliesRenderData{
		sievePageData: sievePage(ctx, ctx.T("filters.autoreply"), filters.VacationScript),
		Replies:       replies.Items,
		Active:        -1,
		Index:         -1,
		Identities:    append([]string{ctx.Login.Username()}, settings.Identities...),
	}
	err = ctx.DoSieve(func(c alborz.SieveClient) error {
		data.CanReply = filters.HasExtension(c, "vacation")
		data.CanDate = filters.HasExtension(c, "date") && filters.HasExtension(c, "relational")
		data.CanWire = filters.HasExtension(c, "include")
		var err error
		data.Active, data.Loaded, data.ByHand, err = filters.ActiveReply(c, replies.Items)
		return err
	})
	return data, err
}

func handleReplies(ctx *web.Context) error {
	data, err := repliesData(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "filters/autoreply", data)
}

func handleReplyForm(ctx *web.Context) error {
	data, err := repliesData(ctx)
	if err != nil {
		return err
	}
	data.Editing = filters.Reply{Days: filters.ReplyDays, Addresses: data.Identities}
	if at := ctx.Param("index"); at != "" {
		i, err := strconv.Atoi(at)
		if err != nil || i < 0 || i >= len(data.Replies) {
			return alborz.NotFound("notfound.reply")
		}
		data.Editing, data.Index = data.Replies[i], i
		shown, err := json.Marshal(data.Editing)
		if err != nil {
			return err
		}
		data.Shown = string(shown)
	}
	data.From, data.Until = ctx.Dates().Day(data.Editing.From), ctx.Dates().Day(data.Editing.Until)
	return ctx.Render(http.StatusOK, "filters/autoreply-edit", data)
}

func replyFromForm(ctx *web.Context) (filters.Reply, error) {
	params, err := ctx.FormParams()
	if err != nil {
		return filters.Reply{}, err
	}
	v := filters.Reply{
		Name:      strings.TrimSpace(ctx.FormValue("name")),
		Subject:   strings.TrimSpace(ctx.FormValue("subject")),
		Text:      strings.TrimRight(strings.ReplaceAll(ctx.FormValue("text"), "\r\n", "\n"), "\n"),
		Addresses: params["addresses"],
	}
	v.Days, err = locale.ReadInt(ctx.FormValue("days"))
	switch {
	case v.Name == "":
		return v, errors.New(ctx.T("form.nameneeded"))
	case v.Text == "":
		return v, errors.New(ctx.T("filters.replyneeded"))
	case err != nil || v.Days < 1 || v.Days > filters.MaxReplyDays:
		return v, fmt.Errorf(ctx.T("filters.baddays"), filters.MaxReplyDays)
	}
	for name, bound := range map[string]*string{"from": &v.From, "until": &v.Until} {
		field := ctx.DateField(name)
		if field.Value == "" {
			continue
		}
		day, err := field.Read(time.UTC)
		if err != nil {
			return v, errors.New(ctx.T("filters.baddate"))
		}
		*bound = day.Format(time.DateOnly)
	}
	if v.From != "" && v.Until != "" && v.Until < v.From {
		return v, errors.New(ctx.T("filters.baddate"))
	}
	return v, nil
}

func handleReplySave(ctx *web.Context) error {
	index := -1
	if ctx.FormValue("index") != "" {
		var err error
		if index, err = formIndex(ctx); err != nil {
			return err
		}
	}
	v, err := replyFromForm(ctx)
	refuse := func(message string) error {
		data, err := repliesData(ctx)
		if err != nil {
			return err
		}
		data.Editing, data.Index, data.Shown = v, index, ctx.FormValue("shown")
		data.From, data.Until = ctx.DateField("from"), ctx.DateField("until")
		return web.RenderRefused(ctx, "filters/autoreply-edit", data, message)
	}
	if err != nil {
		return refuse(err.Error())
	}
	// What the form changed is what it sends that differs from what it showed; the rest stays as stored.
	var drawn *filters.Reply
	var shown filters.Reply
	if ctx.FormValue("shown") != "" {
		if _, err := web.DecodeForm(ctx, "shown", &shown, "the form's shown reply does not decode"); err != nil {
			return err
		}
		drawn = &shown
	}
	var was []filters.Reply
	made := -1
	err = filters.UpdateReplies(ctx.Request().Context(), ctx.Login, func(replies *filters.Replies) error {
		was = slices.Clone(replies.Items)
		if drawn != nil && index >= 0 && index < len(replies.Items) {
			stored := &replies.Items[index]
			web.KeepChanges(reflect.ValueOf(stored).Elem(), reflect.ValueOf(*drawn), reflect.ValueOf(v))
			v = *stored
			return nil
		}
		var err error
		made, err = replies.Put(index, v)
		return err
	})
	if errors.Is(err, filters.ErrTooManyReplies) {
		return answer(ctx, fmt.Errorf(ctx.T("filters.toomanyreplies"), filters.MaxReplies), "filters.autoreply", "")
	}
	if err != nil {
		return refuse(ctx.SaveRefusal(err))
	}
	// An edit to the reply being sent goes out at once; any other waits for activating.
	sent := false
	loaded := ctx.FormValue("loaded")
	if loaded != "" && index >= 0 {
		err = ctx.DoSieve(func(c alborz.SieveClient) error {
			var err error
			sent, err = filters.ReplaceReply(c, loaded, was, index, v)
			return err
		})
	}
	if err == nil && made >= 0 {
		ctx.Made(ctx.T("notice.autoreplycreated"), v.Name,
			ctx.URL("filters.autoreply.reply", made),
			ctx.URL("filters.autoreply.create"))
		return ctx.FoundRedirect(ctx.URL("filters.autoreply"))
	}
	saved := ctx.T("notice.autoreplyinactive")
	if sent {
		saved = ctx.T("notice.autoreplysaved")
	}
	return answer(ctx, err, "filters.autoreply", saved)
}

func handleReplyDelete(ctx *web.Context) error {
	index, err := formIndex(ctx)
	if err != nil {
		return err
	}
	var gone filters.Reply
	err = filters.UpdateReplies(ctx.Request().Context(), ctx.Login, func(replies *filters.Replies) error {
		if index < 0 || index >= len(replies.Items) {
			return alborz.NotFound("notfound.reply")
		}
		gone = replies.Items[index]
		replies.Items = append(replies.Items[:index], replies.Items[index+1:]...)
		return nil
	})
	if errors.Is(err, alborz.ErrNotFound) {
		return err
	}
	if err != nil {
		return ctx.NotifyRedirect(web.Notice{Kind: web.NoticeFailed, Text: ctx.SaveRefusal(err)}, ctx.NextOr(ctx.URL("filters.autoreply")))
	}
	// Deleting the reply being sent stops it.
	loaded := ctx.FormValue("loaded")
	if loaded != "" {
		err = ctx.DoSieve(func(c alborz.SieveClient) error {
			return filters.WithdrawReply(c, loaded, gone)
		})
	}
	return answer(ctx, err, "filters.autoreply", ctx.T("notice.autoreplydeleted"))
}

// An empty index composes no reply, which deletes the script.
func handleReplyActivate(ctx *web.Context) error {
	replies, err := filters.LoadReplies(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	loaded := ctx.FormValue("loaded")
	at := ctx.FormValue("index")
	if at == "" {
		err = ctx.DoSieve(func(c alborz.SieveClient) error {
			return filters.DeactivateReply(c, loaded)
		})
		return answer(ctx, err, "filters.autoreply", ctx.T("notice.autoreplyoff"))
	}
	index, err := strconv.Atoi(at)
	if err != nil || index < 0 || index >= len(replies.Items) {
		return alborz.NotFound("notfound.reply")
	}
	err = ctx.DoSieve(func(c alborz.SieveClient) error {
		return filters.ActivateReply(c, loaded, replies.Items[index])
	})
	return answer(ctx, err, "filters.autoreply", ctx.T("notice.autoreplyon"))
}
