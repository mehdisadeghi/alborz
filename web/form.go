package web

import (
	"io"
	"mime/multipart"
	"net/textproto"
	"net/url"

	"git.mehdix.org/alborz/internal/seal"
)

// Form is a multipart form read as it arrives: fields in memory, files sealed on disk, never spilled
// unsealed to the temp dir as net/http would (ADR 65).
type Form struct {
	Value url.Values
	File  map[string][]*File
}

// File is a file a form carried; Close lets it go.
type File struct {
	Filename string
	Header   textproto.MIMEHeader
	*seal.Spool
}

// ReadForm reads a whole form, bounded by what bounds r; the caller closes it.
func ReadForm(r *multipart.Reader) (*Form, error) {
	form := &Form{Value: url.Values{}, File: map[string][]*File{}}
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			return form, nil
		}
		if err == nil {
			err = form.add(part)
		}
		if err != nil {
			form.Close()
			return nil, err
		}
	}
}

func (form *Form) add(part *multipart.Part) error {
	name := part.FormName()
	if name == "" {
		return nil
	}
	if part.FileName() == "" {
		b, err := io.ReadAll(part)
		form.Value.Add(name, string(b))
		return err
	}
	body, err := seal.NewSpool(func(w io.Writer) error {
		_, err := io.Copy(w, part)
		return err
	})
	if err != nil {
		return err
	}
	form.File[name] = append(form.File[name], &File{Filename: part.FileName(), Header: part.Header, Spool: body})
	return nil
}

// Close lets every file go; nil holds none.
func (form *Form) Close() {
	if form == nil {
		return
	}
	for _, files := range form.File {
		for _, f := range files {
			f.Close()
		}
	}
}
