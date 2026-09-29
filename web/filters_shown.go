package web

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"

	"git.mehdix.org/alborz"
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
	return pageHref(ctx.Request().URL, n)
}

func pageHref(at *url.URL, n int) string {
	q := at.Query()
	q.Del("page")
	if n > 0 {
		q.Set("page", strconv.Itoa(n))
	}
	if len(q) == 0 {
		return at.Path
	}
	return at.Path + "?" + Query(q)
}

const MaxMessagesPerPage = 100

// Three steps, "a few more" or "the lot", as links that need no script.
var perPageLadder = []int{25, 50, 100}

func perPageOptions(chosen int) []int {
	seen := map[int]bool{}
	var out []int
	for _, n := range append(append([]int{}, perPageLadder...), chosen) {
		if n <= 0 || n > MaxMessagesPerPage || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// PerPageChoices are the page sizes a list's menu offers.
func PerPageChoices(ctx *Context) []int {
	return perPageOptions(ctx.Reading().MessagesPerPage)
}

// PerPage is how many rows a page of a list holds, the reader's rather than the account's.
func PerPage(ctx *Context) int {
	return perPage(ctx, ctx.QueryParams())
}

func perPage(ctx *Context, q url.Values) int {
	if raw := q.Get("ipp"); raw != "" {
		if n, err := ReadInt(raw); err == nil &&
			n > 0 && n <= MaxMessagesPerPage {
			return n
		}
	}
	if n := ctx.Reading().MessagesPerPage; n > 0 && n <= MaxMessagesPerPage {
		return n
	}
	return alborz.DefaultMessagesPerPage
}

// Page is one page of a list: its number, counted from zero, and how many rows a page holds.
type Page struct {
	Number, Size int
}

// Within is the page in a list of total rows. A page past the end is the
// last one: the list may have shrunk since the link was drawn.
func (p Page) Within(total int) Page {
	p.Number = min(p.Number, max(0, (total-1)/p.Size))
	return p
}

// Rows are the bounds of the page's rows in a list of total rows.
func (p Page) Rows(total int) (from, to int) {
	from = min(p.Number*p.Size, total)
	return from, min(from+p.Size, total)
}

// Window is how many rows lie from the top of the list to the end of this page.
func (p Page) Window() int {
	return (p.Number + 1) * p.Size
}

// ListPage is the page of a list the request asks for; a number it cannot read is refused.
func (ctx *Context) ListPage() (Page, error) {
	return ctx.ListPageAt(ctx.QueryParams())
}

// ListPageAt is the page a list's address asks for, read as ListPage reads the request's.
func (ctx *Context) ListPageAt(q url.Values) (Page, error) {
	p := Page{Size: perPage(ctx, q)}
	if raw := q.Get("page"); raw != "" {
		n, err := ReadInt(raw)
		if err != nil || n < 0 {
			return p, NewError(http.StatusBadRequest, "invalid page index")
		}
		p.Number = n
	}
	return p, nil
}

// Pager is where one page lies in its list, and the addresses of the pages beside it.
type Pager struct {
	From, To, Total int
	Prev, Next      string

	Per     int
	Options []int
	IppBase string
}

// NewPager is the pager of page p of a list of total rows, drawn at the address at.
func NewPager(at *url.URL, p Page, total int, sizes []int) Pager {
	from, to := p.Rows(total)
	q := at.Query()
	q.Del("ipp")
	q.Del("page")
	pager := Pager{To: to, Total: total, Per: p.Size, Options: sizes}
	if len(q) > 0 {
		pager.IppBase = "&" + Query(q)
	}
	if total > 0 {
		pager.From = from + 1
	}
	if p.Number > 0 {
		pager.Prev = pageHref(at, p.Number-1)
	}
	if to < total {
		pager.Next = pageHref(at, p.Number+1)
	}
	return pager
}

// Paginate returns the requested page of a list held whole, and its pager.
func Paginate[T any](ctx *Context, items []T) ([]T, Pager, error) {
	p, err := ctx.ListPage()
	if err != nil {
		return nil, Pager{}, err
	}
	p = p.Within(len(items))
	from, to := p.Rows(len(items))
	return items[from:to], NewPager(ctx.Request().URL, p, len(items), PerPageChoices(ctx)), nil
}

// Neighbours are the objects either side of the one on the page; Total is zero without a list.
type Neighbours struct {
	PrevURL  string
	NextURL  string
	Position int
	Total    int
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
