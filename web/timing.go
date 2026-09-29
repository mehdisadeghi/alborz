package web

import (
	"context"
	"time"

	"github.com/emersion/go-smtp"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2/imapclient"
)

// DoIMAP runs the IMAP operation and times it.
func (ctx *Context) DoIMAP(f func(*imapclient.Client) error) error {
	return ctx.doIMAPWithin(alborz.RoundTripTimeout, f)
}

// DoIMAPScan runs a whole-folder search, which may outlast a round trip, off the connection the reader's clicks wait on.
func (ctx *Context) DoIMAPScan(f func(*imapclient.Client) error) error {
	return ctx.Login.DoIMAPWork(ctx.Request().Context(), alborz.IMAPScan, alborz.ScanTimeout, f)
}

func (ctx *Context) doIMAPWithin(bound time.Duration, f func(*imapclient.Client) error) error {
	start := time.Now()
	err := ctx.Login.DoIMAPContext(ctx.Request().Context(), bound, f)
	ctx.timing.Add("imap", start, time.Now())
	return err
}

// DoSieve runs the ManageSieve operation and times it.
func (ctx *Context) DoSieve(f func(alborz.SieveClient) error) error {
	start := time.Now()
	err := ctx.Login.DoSieve(f)
	ctx.timing.Add("sieve", start, time.Now())
	return err
}

// DoSMTP runs the SMTP operation and times it.
func (ctx *Context) DoSMTP(f func(*smtp.Client) error) error {
	start := time.Now()
	err := ctx.Login.DoSMTP(f)
	ctx.timing.Add("smtp", start, time.Now())
	return err
}

// installTiming also puts the Timing on the request context, so plugin
// round-trippers reach it.
func (ctx *Context) installTiming() {
	ctx.timing = alborz.NewTiming()
	req := ctx.Request()
	ctx.SetRequest(req.WithContext(context.WithValue(req.Context(), alborz.TimingKey, ctx.timing)))
	ctx.Response().Before(func() {
		if h := ctx.timing.Header(); h != "" {
			ctx.Response().Header().Set("Server-Timing", h)
		}
	})
}
