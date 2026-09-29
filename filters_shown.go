package alborz

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
)

// Filter is one narrowing in force on a listing, with the page without it.
type Filter struct {
	Label string
	Value string
	Href  string
}

// FilterRow is one entry of a list's filter menu; a row in force links to
// the page without it.
type FilterRow struct {
	Label   string
	Href    string
	Current bool
	// Star is the colour a row names.
	Star string
}

// WithoutParam returns this page with one query parameter and the page number dropped.
func (ctx *Context) WithoutParam(name string) string {
	return ctx.WithParam(name, "")
}

// WithParam returns this page with one query parameter set, or dropped when
// value is empty, and the page number dropped.
func (ctx *Context) WithParam(name, value string) string {
	u := *ctx.Request().URL
	q := u.Query()
	q.Del(name)
	if value != "" {
		q.Set(name, value)
	}
	q.Del("page")
	u.RawQuery = Query(q)
	u.Path = ctx.Request().URL.Path
	if u.RawQuery == "" {
		return u.Path
	}
	return u.Path + "?" + u.RawQuery
}

// FilterOn returns the filter for a set parameter, or false.
func (ctx *Context) FilterOn(name, label string) (Filter, bool) {
	value := ctx.QueryParam(name)
	if value == "" {
		return Filter{}, false
	}
	return Filter{Label: label, Value: value, Href: ctx.WithoutParam(name)}, true
}

// PageHref returns this page at page n; the first page carries no number.
func (ctx *Context) PageHref(n int) string {
	u := *ctx.Request().URL
	q := u.Query()
	q.Del("page")
	if n > 0 {
		q.Set("page", strconv.Itoa(n))
	}
	u.RawQuery = Query(q)
	if u.RawQuery == "" {
		return u.Path
	}
	return u.Path + "?" + u.RawQuery
}

// FlagColors names the colours the flag bits address, indexed by their value.
var FlagColors = [7]string{"red", "orange", "yellow", "green", "blue", "purple", "grey"}

// Views narrow a folder by a flag.
const (
	ViewStarred = "starred"
	ViewUnread  = "unread"
)

// StarMatches says whether a mark answers a view.
func StarMatches(star, view string) bool {
	switch view {
	case "":
		return true
	case ViewStarred:
		return star != ""
	}
	return star == view
}

// StarLeaves says whether a row with this star leaves the list at next.
func StarLeaves(next, star string) bool {
	u, err := url.Parse(next)
	if err != nil {
		return false
	}
	view := u.Query().Get("view")
	return (view == ViewStarred || slices.Contains(FlagColors[:], view)) && !StarMatches(star, view)
}

// StarNotice says what became of a row's star that left its list.
func StarNotice(ctx *Context, star, action string, undo url.Values) Notice {
	text := ctx.T("notice.starremoved")
	if star != "" {
		text = fmt.Sprintf(ctx.T("notice.starchanged"), ctx.T("color."+star))
	}
	return Notice{Kind: NoticeDone, Text: text, Actions: []*NoticeAction{ctx.Undo(action, undo)}}
}

// ViewRows are the views a list's filter menu offers.
func ViewRows(ctx *Context, current string, unread bool, clear string) []FilterRow {
	row := func(label, view, star string) FilterRow {
		href := ctx.WithParam("view", view)
		if view == current {
			href = clear
		}
		return FilterRow{Label: label, Href: href, Current: view == current, Star: star}
	}
	var rows []FilterRow
	if unread {
		rows = append(rows, row(ctx.T("mailbox.unread"), ViewUnread, ""))
	}
	rows = append(rows, row(ctx.T("mailbox.starred"), ViewStarred, ""))
	for _, name := range FlagColors {
		rows = append(rows, row(ctx.T("color."+name), name, name))
	}
	return rows
}
