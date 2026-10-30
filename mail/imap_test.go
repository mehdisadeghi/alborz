package mail

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestFetchOneRefreshesAStaleSelectedMailbox(t *testing.T) {
	for _, tc := range []struct {
		name      string
		commands  []string
		responses []string
		wantError bool
		missing   bool
	}{
		{"already visible", []string{"UID FETCH"}, []string{"* 1 FETCH (UID 1)\r\n"}, false, false},
		{"visible after noop", []string{"UID FETCH", "NOOP", "UID FETCH"}, []string{"", "* 1 EXISTS\r\n", "* 1 FETCH (UID 1)\r\n"}, false, false},
		{"still missing", []string{"UID FETCH", "NOOP", "UID FETCH"}, []string{"", "", ""}, true, true},
		{"first fetch refused", []string{"UID FETCH"}, []string{"NO"}, true, false},
		{"noop refused", []string{"UID FETCH", "NOOP"}, []string{"", "NO"}, true, false},
		{"retry refused", []string{"UID FETCH", "NOOP", "UID FETCH"}, []string{"", "", "NO"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			// A broken command sequence fails promptly instead of hanging the suite.
			client.SetDeadline(time.Now().Add(5 * time.Second))
			server.SetDeadline(time.Now().Add(5 * time.Second))
			done := make(chan error, 1)
			go func() {
				defer server.Close()
				if _, err := fmt.Fprint(server, "* PREAUTH [CAPABILITY IMAP4rev1] Ready\r\n"); err != nil {
					done <- err
					return
				}
				r := bufio.NewReader(server)
				for i, command := range tc.commands {
					line, err := r.ReadString('\n')
					if err != nil {
						done <- err
						return
					}
					tag, text, _ := strings.Cut(line, " ")
					if !strings.HasPrefix(strings.TrimSpace(text), command) {
						done <- fmt.Errorf("got %q, want %s", line, command)
						return
					}
					response := tc.responses[i]
					if response == "NO" {
						_, err = fmt.Fprintf(server, "%s NO Refused\r\n", tag)
					} else {
						_, err = fmt.Fprintf(server, "%s%s OK Done\r\n", response, tag)
					}
					if err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			c := imapclient.New(client, nil)
			defer c.Close()
			msg, err := fetchOne(c, 1, &imap.FetchOptions{UID: true})
			if (err != nil) != tc.wantError {
				t.Errorf("fetch: %v, want error %v", err, tc.wantError)
			}
			if !tc.wantError && (msg == nil || msg.UID != 1) {
				t.Errorf("fetched %v, want UID 1", msg)
			}
			if tc.missing {
				if _, ok := errors.AsType[*alborz.NotFoundError](err); !ok {
					t.Errorf("missing UID: %v, want not found", err)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
