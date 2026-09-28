package alborzbase

import (
	"fmt"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-message"
)

// ErrViewUnsupported is returned when a viewer does not support a message part.
var ErrViewUnsupported = fmt.Errorf("cannot generate message view: unsupported part")

// View is a message part as the page shows it.
type View struct {
	Body interface{}
	// Remote says the part asks for resources elsewhere.
	Remote bool
}

// Viewer is a message part viewer.
type Viewer interface {
	// ViewMessagePart renders the message part at path, or returns ErrViewUnsupported.
	ViewMessagePart(*alborz.Context, *IMAPMessage, []int, *message.Entity) (View, error)
}

func (p *plugin) viewMessagePart(ctx *alborz.Context, msg *IMAPMessage, path []int, part *message.Entity) (View, error) {
	for _, viewer := range p.viewers {
		v, err := viewer.ViewMessagePart(ctx, msg, path, part)
		if err == ErrViewUnsupported {
			continue
		}
		return v, err
	}
	return View{}, ErrViewUnsupported
}
