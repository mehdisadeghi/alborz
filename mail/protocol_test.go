package mail

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-smtp"
)

func TestListRequestsSpecialUseRoles(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	server.SetDeadline(time.Now().Add(5 * time.Second))
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		fmt.Fprint(server, "* PREAUTH [CAPABILITY IMAP4rev1 LIST-EXTENDED SPECIAL-USE] Ready\r\n")
		line, err := bufio.NewReader(server).ReadString('\n')
		if err != nil {
			done <- err
			return
		}
		tag, command, _ := strings.Cut(line, " ")
		attrs := ""
		if strings.Contains(command, "RETURN (SPECIAL-USE)") {
			attrs = `\Sent`
		}
		fmt.Fprintf(server, "* LIST (%s) \"/\" \"Gesendet\"\r\n", attrs)
		attrs = ""
		if strings.Contains(command, "RETURN (SPECIAL-USE)") {
			attrs = `\Drafts`
		}
		fmt.Fprintf(server, "* LIST (%s) \"/\" \"Entwuerfe\"\r\n%s OK Done\r\n", attrs, tag)
		if !strings.Contains(command, "RETURN (SPECIAL-USE)") {
			done <- fmt.Errorf("LIST omitted SPECIAL-USE: %s", command)
			return
		}
		done <- nil
	}()
	c := imapclient.New(client, nil)
	defer c.Close()
	folders, err := listMailboxes(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, folder := range folders {
		if folder.Role() == "" {
			t.Errorf("localized %q has no role", folder.Mailbox)
		}
	}
	if len(folders) != 2 {
		t.Errorf("folders=%v", folders)
	}
	if err := <-done; err != nil {
		t.Error(err)
	}
}

func TestSMTPFinalRejectionIsKnownAndTyped(t *testing.T) {
	for _, code := range []int{451, 554, 0} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			client.SetDeadline(time.Now().Add(5 * time.Second))
			server.SetDeadline(time.Now().Add(5 * time.Second))
			done := make(chan error, 1)
			go func() {
				defer server.Close()
				r := bufio.NewReader(server)
				fmt.Fprint(server, "220 localhost Ready\r\n")
				for _, step := range []struct{ command, response string }{{"EHLO", "250 localhost\r\n"}, {"MAIL FROM", "250 OK\r\n"}, {"RCPT TO", "250 OK\r\n"}, {"DATA", "354 Continue\r\n"}} {
					line, err := r.ReadString('\n')
					if err != nil {
						done <- err
						return
					}
					if !strings.HasPrefix(line, step.command) {
						done <- fmt.Errorf("got %q, want %q", line, step.command)
						return
					}
					fmt.Fprint(server, step.response)
				}
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						done <- err
						return
					}
					if line == ".\r\n" {
						break
					}
				}
				if code != 0 {
					fmt.Fprintf(server, "%d Rejected\r\n", code)
				}
				done <- nil
			}()
			c := smtp.NewClient(client)
			defer c.Close()
			uncertain := false
			err := sendMessage(c, &OutgoingMessage{From: "from@example.org", To: []string{"to@example.org"}}, strings.NewReader("Subject: test\r\n\r\nhello\r\n"), &uncertain)
			if err == nil {
				t.Fatal("expected rejection or lost response")
			}
			if uncertain != (code == 0) {
				t.Errorf("uncertain=%v for final code %d: %v", uncertain, code, err)
			}
			var rejected *smtp.SMTPError
			if code != 0 && (!errors.As(err, &rejected) || rejected.Code != code) {
				t.Errorf("SMTP status type lost: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
