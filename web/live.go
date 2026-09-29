package web

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
)

// livePing keeps proxies from dropping an idle stream.
const livePing = 25 * time.Second

func handleEvents(ctx *Context) error {
	s := ctx.Session()
	// The stream outlives its request, so each change rechecks the session's accounts.
	mine := func(account string) bool {
		live, _ := s.Accounts()
		return slices.ContainsFunc(live, func(s *alborz.Login) bool { return s.Username() == account })
	}
	changes, stop := ctx.Server.Changes.Listen()
	defer stop()

	res := ctx.Response()
	res.Header().Set("Content-Type", "text/event-stream")
	res.Header().Set("Cache-Control", "no-cache")
	res.Header().Set("X-Accel-Buffering", "no")
	res.WriteHeader(http.StatusOK)
	res.Flush()

	ping := time.NewTicker(livePing)
	defer ping.Stop()
	for {
		if s.Locked(time.Now()) {
			fmt.Fprint(res, "event: locked\ndata:\n\n")
			res.Flush()
			return nil
		}
		select {
		case <-ctx.Request().Context().Done():
			return nil
		case <-ping.C:
			if _, err := fmt.Fprint(res, ": ping\n\n"); err != nil {
				return nil
			}
			res.Flush()
		case change, ok := <-changes:
			if !ok {
				return nil
			}
			if !mine(change.Account) {
				continue
			}
			// A newline in either value would end the frame.
			_, err := fmt.Fprintf(res, "event: mailbox\ndata: %s %s\n\n",
				oneLine(change.Account), oneLine(change.Mailbox))
			if err != nil {
				return nil
			}
			res.Flush()
		}
	}
}

func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
