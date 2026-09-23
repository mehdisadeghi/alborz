package alborzbase

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// refusingIMAP advertises NOTIFY and answers it with BAD, and speaks
// just enough else for the watcher's fallback to run. The fork's own
// server will not advertise what it does not implement, so a server
// that claims NOTIFY and refuses it has to be scripted.
func refusingIMAP(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		say := func(s string) { fmt.Fprint(conn, s+"\r\n") }
		var idleTag string
		say("* OK [CAPABILITY IMAP4rev1 IDLE NOTIFY] refusing")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			if strings.EqualFold(f[0], "DONE") {
				say(idleTag + " OK idle done")
				continue
			}
			tag, cmd := f[0], strings.ToUpper(f[1])
			switch cmd {
			case "LOGIN":
				say(tag + " OK [CAPABILITY IMAP4rev1 IDLE NOTIFY] in")
			case "CAPABILITY":
				say("* CAPABILITY IMAP4rev1 IDLE NOTIFY")
				say(tag + " OK")
			case "SELECT", "EXAMINE":
				say("* 0 EXISTS")
				say("* OK [UIDVALIDITY 1] v")
				say("* OK [UIDNEXT 1] n")
				say(tag + " OK [READ-ONLY] selected")
			case "NOTIFY":
				say(tag + " BAD NOTIFY is not implemented here")
			case "LIST":
				say(`* LIST () "/" INBOX`)
				say(`* LIST () "/" Junk`)
				say(tag + " OK")
			case "STATUS":
				say(`* STATUS Junk (UIDNEXT 7)`)
				say(tag + " OK")
			case "IDLE":
				idleTag = tag
				say("+ idling")
			default:
				say(tag + " OK")
			}
		}
	}()
	return ln.Addr().String()
}

// A server that advertises NOTIFY and then refuses it is watched by
// sweeping, on a connection the refusal did not break (ADR 45).
func TestNotifyAdvertisedButRefused(t *testing.T) {
	c, err := imapclient.DialInsecure(refusingIMAP(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("u", "p").Wait(); err != nil {
		t.Fatal(err)
	}
	if !c.Caps().Has(imap.CapNotify) {
		t.Fatal("the server does not advertise NOTIFY; the check proves nothing")
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if armNotify(c) {
		t.Fatal("a refused NOTIFY was taken as armed")
	}
	if err := folderSweep("u", c, nil)(); err != nil {
		t.Fatalf("the sweep failed after a refused NOTIFY: %v", err)
	}
	idle, err := c.Idle()
	if err != nil {
		t.Fatalf("IDLE failed after a refused NOTIFY: %v", err)
	}
	if err := idle.Close(); err != nil {
		t.Fatalf("leaving IDLE failed: %v", err)
	}
}
