package web

import (
	_ "embed"
	"encoding/json"
)

// An error here is a bug: the values are paths this process built.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Saved pages are mail on disk, so signing in or out, PAGES_DAYS and PAGES_MAX bound them.
//
//go:embed serviceworker.js
var serviceWorker string
