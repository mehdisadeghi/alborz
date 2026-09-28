package pim

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"github.com/labstack/echo/v4"
)

// Column is one order a list can take: its URL key and a row's sort value.
type Column[T any] struct {
	Key   string
	Value func(T) string
}

// Last sorts after every value, for a row that has none.
const Last = "\uffff"

// When is a time as a column sorts it; a row without one sorts Last.
func When(t time.Time) string {
	if t.IsZero() {
		return Last
	}
	return t.Format(time.RFC3339)
}

// StarredFirst sorts marked rows first, as the mail list does by flag.
func StarredFirst(star string) string {
	if star != "" {
		return "0"
	}
	return "1"
}

// Sorting is a list's order; Explicit and DirExplicit say whether the URL asked for it.
type Sorting struct {
	Key, Dir              string
	Explicit, DirExplicit bool
	// Params is what a sort link keeps of the list's address; Reset is the default order.
	Params, Reset string
}

// A new order starts at the first page, so sort links drop these.
var sortDrops = []string{"sort", "dir", "page"}

// Sort orders rows as the URL asks, by the first column by default; tie breaks equal rows.
func Sort[T any](ctx *alborz.Context, rows []T, columns []Column[T], tie func(T) string, keep ...string) (Sorting, error) {
	key, dir := ctx.QueryParam("sort"), ctx.QueryParam("dir")
	s := Sorting{Key: key, Dir: dir, Explicit: key != "", DirExplicit: dir != "", Reset: "?"}
	if key == "" {
		s.Key = columns[0].Key
	}
	at := slices.IndexFunc(columns, func(c Column[T]) bool { return c.Key == s.Key })
	if at < 0 {
		return s, echo.NewHTTPError(http.StatusBadRequest, "invalid sort order")
	}
	if dir != "" && dir != "asc" && dir != "desc" {
		return s, echo.NewHTTPError(http.StatusBadRequest, "invalid sort direction")
	}
	kept := Keep(ctx.QueryParams(), keep...)
	for _, k := range sortDrops {
		kept.Del(k)
	}
	if len(kept) > 0 {
		s.Params = "&" + alborz.Query(kept)
		s.Reset = "?" + alborz.Query(kept)
	}
	value := columns[at].Value
	slices.SortStableFunc(rows, func(a, b T) int {
		x, y := value(a), value(b)
		if x == y {
			x, y = tie(a), tie(b)
		}
		if dir == "desc" {
			x, y = y, x
		}
		return strings.Compare(x, y)
	})
	return s, nil
}

// Pager is one page of a list held whole, since a DAV query cannot ask for rows after the first n.
type Pager struct {
	From, To, Total int
	Prev, Next      string

	Per     int
	Options []int
	IppBase string
}

// Paginate returns the requested page of items and the pager for the rest.
func Paginate[T any](ctx *alborz.Context, items []T) ([]T, Pager) {
	per := mail.PerPage(ctx)
	page, err := alborz.ReadInt(ctx.QueryParam("page"))
	if err != nil || page < 0 {
		page = 0
	}
	// A page past the end is the last one: the list may have shrunk since the link was drawn.
	if last := max(0, (len(items)-1)/per); page > last {
		page = last
	}
	from, to := page*per, min((page+1)*per, len(items))
	q := ctx.Request().URL.Query()
	q.Del("ipp")
	q.Del("page")
	base := ""
	if len(q) > 0 {
		base = "&" + alborz.Query(q)
	}
	pager := Pager{From: from + 1, To: to, Total: len(items),
		Per: per, Options: mail.PerPageChoices(ctx), IppBase: base}
	if len(items) == 0 {
		pager.From = 0
	}
	if page > 0 {
		pager.Prev = ctx.PageHref(page - 1)
	}
	if to < len(items) {
		pager.Next = ctx.PageHref(page + 1)
	}
	return items[from:to], pager
}
