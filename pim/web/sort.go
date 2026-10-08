package web

import (
	"net/http"
	"slices"

	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/web"
)

// Sorting is a list's order, Dir the direction in force; Explicit and DirExplicit say whether the URL asked for it.
type Sorting struct {
	Key, Dir              string
	Explicit, DirExplicit bool
	// Params is what a sort link keeps of the list's address; Reset is the default order.
	Params, Reset string
}

// A new order starts at the first page, so sort links drop these.
var sortDrops = []string{"sort", "dir", "page"}

// SortList applies the shared list state to section columns and preserves the
// same order contract for each section's projection.
func SortList[T any](state web.ListState, rows []T, columns []pim.Column[T], tie func(T) string, keep ...string) (Sorting, error) {
	key, dir := state.Sort, state.Direction
	s := Sorting{Key: key, Dir: dir, Explicit: key != "", DirExplicit: dir != "", Reset: "?"}
	if key == "" {
		s.Key = columns[0].Key
	}
	at := slices.IndexFunc(columns, func(c pim.Column[T]) bool { return c.Key == s.Key })
	if at < 0 {
		return s, web.NewError(http.StatusBadRequest, "invalid sort order")
	}
	if dir != "" && dir != "asc" && dir != "desc" {
		return s, web.NewError(http.StatusBadRequest, "invalid sort direction")
	}
	kept := state.Keep(keep...)
	for _, k := range sortDrops {
		kept.Del(k)
	}
	if len(kept) > 0 {
		s.Params = "&" + web.Query(kept)
		s.Reset = "?" + web.Query(kept)
	}
	desc := dir == "desc" || (dir == "" && columns[at].Desc)
	if desc {
		s.Dir = "desc"
	}
	pim.Order(rows, columns[at], tie, desc)
	return s, nil
}
