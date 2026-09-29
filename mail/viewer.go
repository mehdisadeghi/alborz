package mail

import (
	"fmt"

	"git.mehdix.org/alborz/web"
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

func viewMessagePart(ctx *web.Context, msg *IMAPMessage, path []int, part *message.Entity) (View, error) {
	v, err := viewHTML(ctx, msg, part)
	if err != ErrViewUnsupported {
		return v, err
	}
	return viewText(ctx, msg, path, part)
}
