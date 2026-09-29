package pim

import (
	"errors"
	"fmt"
	"path"

	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-webdav"
)

// ErrNoDestination is a create form naming a collection the account lacks or that refuses the object.
var ErrNoDestination = errors.New("dav: no such collection to write into")

// Target is where a form's object is written: a new name only while free, a held one only while unchanged.
func Target(collection, name, held, etag string) (at string, ifMatch, ifNoneMatch webdav.ConditionalMatch) {
	if held == "" {
		return path.Join(collection, name), "", dav.IfNew
	}
	return held, dav.IfUnchanged(etag), ""
}

// ErrNothingToExport is an empty collection, for which no file can be written.
var ErrNothingToExport = errors.New("nothing to export")

// UnreadableError is a file its parser could not read, for the reader to correct.
type UnreadableError struct{ Err error }

func (e *UnreadableError) Error() string { return "failed to read the file: " + e.Err.Error() }

func (e *UnreadableError) Unwrap() error { return e.Err }

// PartlySavedError is an import stopped after Saved of Total objects were written; those stay written.
type PartlySavedError struct {
	Saved, Total int
	Err          error
}

func (e *PartlySavedError) Error() string {
	return fmt.Sprintf("%d of %d saved: %v", e.Saved, e.Total, e.Err)
}

func (e *PartlySavedError) Unwrap() error { return e.Err }

// ImportAnswered says whether an import stopped on the reader's file or the server's answer, rather than alborz failing.
func ImportAnswered(err error) bool {
	var unreadable *UnreadableError
	return errors.As(err, &unreadable) || errors.Is(err, dav.ErrPlaceDown) || dav.ServerRefused(err)
}
