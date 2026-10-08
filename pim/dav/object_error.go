package dav

import (
	"context"
	"errors"
	"fmt"
	"net"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-webdav"
)

// ObjectError preserves protocol and transport errors, and marks malformed objects unreadable.
func ObjectError(ctx context.Context, path string, err error) error {
	var transport net.Error
	if _, answered := webdav.HTTPErrorCode(err); err != nil && !answered && !errors.As(err, &transport) && ctx.Err() == nil {
		return fmt.Errorf("%w: %s: %v", alborz.ErrUnreadable, path, err)
	}
	return err
}
