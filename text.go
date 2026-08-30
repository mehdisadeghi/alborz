package alborz

import (
	"strings"
	"time"
)

// AppendNote puts a dated line under what is already there, keeping the
// two apart with a blank line so the field reads as entries rather than
// as one paragraph that grew. Every object that carries a free-text
// note - an event, a task, a contact - appends the same way, so a note
// written on one page reads like a note written on another.
func AppendNote(existing, note string, when time.Time) string {
	note = strings.ReplaceAll(note, "\r", "")
	stamp := when.Format("2006-01-02 15:04")
	entry := stamp + "\n" + note
	if strings.TrimSpace(existing) == "" {
		return entry
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + entry
}
