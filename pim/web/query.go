package web

import (
	"strings"

	"git.mehdix.org/alborz/search"
	rootweb "git.mehdix.org/alborz/web"
)

// SearchQuery returns the query as entered and its normalized search terms.
// Pages keep Raw for their displayed value while Parsed drives filtering.
func SearchQuery(ctx *rootweb.Context) (raw string, parsed search.Query) {
	raw = ctx.QueryParam("query")
	return raw, search.Parse(strings.TrimSpace(raw))
}
