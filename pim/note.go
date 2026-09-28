package pim

import (
	"strings"
	"time"
)

// AppendNote appends a dated line to a free-text note, separated by a blank line.
func AppendNote(existing, note string, when time.Time) string {
	note = strings.ReplaceAll(note, "\r", "")
	stamp := when.Format("2006-01-02 15:04")
	entry := stamp + "\n" + note
	if strings.TrimSpace(existing) == "" {
		return entry
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + entry
}
