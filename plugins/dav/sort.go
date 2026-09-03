package dav

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/labstack/echo/v4"
)

// Column is one order a list can be put in: the key its URL names the
// order by, and what a row sorts under in it.
type Column[T any] struct {
	Key   string
	Value func(T) string
}

// Last sorts after every value a row can have, for a row that has none:
// an undated task belongs after the dated ones in ascending order.
const Last = "\uffff"

// When is a time as a column sorts it; a row without one sorts Last.
func When(t time.Time) string {
	if t.IsZero() {
		return Last
	}
	return t.Format(time.RFC3339)
}

// Sorting is a list's order as its sort menu and its column headers
// draw it. Key and Dir are the order in force; Explicit and DirExplicit
// say whether the URL asked for them, since a default order marks no
// column and a direction asked for is one press from the default again.
type Sorting struct {
	Key, Dir              string
	Explicit, DirExplicit bool
	// Params is what a sort link keeps of the list's address, written
	// after the link's own sort, and Reset the list in its default order.
	Params, Reset string
}

// Sort puts rows in the order the URL asks for, the first column's when
// it asks for none, and says what the sort controls draw. tie orders
// the rows a column holds equal, and turns with the direction as the
// column does. keep names the parameters a sort link carries on.
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
	kept := url.Values{}
	for _, k := range keep {
		if v := ctx.QueryParam(k); v != "" {
			kept.Set(k, v)
		}
	}
	if len(kept) > 0 {
		s.Params = "&" + alborz.AddressQuery(kept)
		s.Reset = "?" + alborz.AddressQuery(kept)
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
