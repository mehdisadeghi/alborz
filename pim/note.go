package pim

import (
	"strings"
	"time"

	"git.mehdix.org/alborz/web"
)

// noteStamp is ISO 8601 in Gregorian whatever the reader's calendar: the note is stored data other clients read.
const noteStamp = "2006-01-02T15:04Z07:00"

// AppendNote appends a line stamped with the reader's time and offset to a free-text note, separated by a blank line.
func AppendNote(ctx *web.Context, existing, note string) string {
	note = strings.ReplaceAll(note, "\r", "")
	entry := time.Now().In(web.UserLocation(ctx)).Format(noteStamp) + "\n" + note
	if strings.TrimSpace(existing) == "" {
		return entry
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + entry
}
