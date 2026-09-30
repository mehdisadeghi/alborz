package mail

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

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
	if err := New(&alborz.Server{Options: &alborz.Options{Cache: true}}).folderSweep("u", c, nil)(); err != nil {
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

// imapserver always offers IDLE with IMAP4rev1, so it is struck from what the server says.
type idleHiding struct{ net.Listener }

func (l idleHiding) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	return idleHidingConn{c}, err
}

type idleHidingConn struct{ net.Conn }

func (c idleHidingConn) Write(b []byte) (int, error) {
	if _, err := c.Conn.Write(bytes.ReplaceAll(b, []byte(" IDLE"), nil)); err != nil {
		return 0, err
	}
	return len(b), nil
}

func TestWithoutIdleTheSweepNoticesNewMail(t *testing.T) {
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("u", "p")
	for _, name := range []string{"INBOX", "Junk"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(user)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
	})
	go srv.Serve(idleHiding{ln})
	t.Cleanup(func() { srv.Close() })

	c, err := imapclient.DialInsecure(ln.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("u", "p").Wait(); err != nil {
		t.Fatal(err)
	}
	if c.Caps().Has(imap.CapIdle) {
		t.Fatal("the server advertises IDLE; the check proves nothing")
	}

	p := New(&alborz.Server{Options: &alborz.Options{Cache: true}})
	for _, name := range []string{"INBOX", "Junk"} {
		p.listings.storeAt("u", name, &listingEntry{perPage: 25}, p.listings.epoch("u"))
	}
	done, swept := make(chan struct{}), make(chan struct{})
	sweep := p.folderSweep("u", c, &alborz.Changes{})
	go sweepEvery(done, func() error {
		err := sweep()
		select {
		case swept <- struct{}{}:
		case <-done:
		}
		return err
	}, time.Millisecond)
	defer close(done)

	<-swept
	for _, name := range []string{"INBOX", "Junk"} {
		if _, state := p.listings.lookup("u", name, 25); state != listingFresh {
			t.Fatalf("%s is %v before any mail came", name, state)
		}
		raw := "Subject: new\r\n\r\nBody.\r\n"
		if _, err := user.Append(name, strings.NewReader(raw), &imap.AppendOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	<-swept
	<-swept
	for _, name := range []string{"INBOX", "Junk"} {
		if _, state := p.listings.lookup("u", name, 25); state != listingChanged {
			t.Errorf("new mail in %s went unnoticed: %v", name, state)
		}
	}
}
