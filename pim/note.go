package pim

import (
	"strings"
	"time"
)

// noteStamp is ISO 8601 in Gregorian whatever the reader's calendar: the note is stored data other clients read.
const noteStamp = "2006-01-02T15:04Z07:00"

// AppendNote appends a line stamped with now, the reader's time and offset, to a free-text note, separated by a blank line.
func AppendNote(existing, note string, now time.Time) string {
	note = strings.ReplaceAll(note, "\r", "")
	entry := now.Format(noteStamp) + "\n" + note
	if strings.TrimSpace(existing) == "" {
		return entry
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + entry
}
