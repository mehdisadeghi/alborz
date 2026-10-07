package pim

import (
	"errors"
	"fmt"
	"io"
	"net/http"
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

// Reapplied runs a read-change-write once more when another write landed between its read and its
// PUT (RFC 7232 4.2): a one-property change applies as well to the fresh read.
func Reapplied[T any](write func() (T, error)) (T, error) {
	v, err := write()
	if code, _ := webdav.HTTPErrorCode(err); code == http.StatusPreconditionFailed {
		return write()
	}
	return v, err
}

// ErrNothingToExport is an empty collection, for which no file can be written.
var ErrNothingToExport = errors.New("nothing to export")

// UnreadableError is a file its parser could not read, for the reader to correct.
type UnreadableError struct{ Err error }

func (e *UnreadableError) Error() string { return "failed to read the file: " + e.Err.Error() }

func (e *UnreadableError) Unwrap() error { return e.Err }

// PartlySavedError is an import that wrote Saved of Total objects; Err says why the rest were not.
type PartlySavedError struct {
	Saved, Total int
	Err          error
}

func (e *PartlySavedError) Error() string {
	return fmt.Sprintf("%d of %d saved: %v", e.Saved, e.Total, e.Err)
}

func (e *PartlySavedError) Unwrap() error { return e.Err }

// LeftOutError is an export that left Left objects out; Err says why each was.
type LeftOutError struct {
	Left int
	Err  error
}

func (e *LeftOutError) Error() string { return fmt.Sprintf("%d left out: %v", e.Left, e.Err) }

func (e *LeftOutError) Unwrap() error { return e.Err }

// MaxImportSize is the most an import reads of one file, whatever it came from: an import is held
// in memory while parsed and written.
const MaxImportSize = 16 << 20

// ErrTooLarge is a file over MaxImportSize.
var ErrTooLarge = errors.New("the file is too large to import")

// ReadImport reads a file to import, refusing one over MaxImportSize.
func ReadImport(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxImportSize+1))
	if err == nil && len(raw) > MaxImportSize {
		return nil, ErrTooLarge
	}
	return raw, err
}

// ObjectRefused says the file or the server turned one object of an import away, so the rest go on.
func ObjectRefused(err error) bool {
	var unreadable *UnreadableError
	return errors.As(err, &unreadable) || dav.ServerRefused(err)
}

// Saved ends an import that wrote n of total objects; refused says why each of the others was not.
func Saved(n, total int, refused []error) (int, error) {
	if len(refused) == 0 {
		return n, nil
	}
	return n, &PartlySavedError{Saved: n, Total: total, Err: errors.Join(refused...)}
}

// ImportAnswered says whether an import stopped on the reader's file or the server's answer, rather than alborz failing.
func ImportAnswered(err error) bool {
	var unreadable *UnreadableError
	return errors.As(err, &unreadable) || errors.Is(err, ErrTooLarge) || errors.Is(err, dav.ErrPlaceDown) || dav.ServerRefused(err)
}
