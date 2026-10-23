package mail

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

func TestNotificationTextIsBoundedAndPlain(t *testing.T) {
	text := notificationHTML(`<head><title>private metadata</title></head><p>Hello &amp; welcome</p><script>bad()</script><style>secret</style><img src="https://tracker.invalid/">`)
	if got := notificationText(text, 180); got != "Hello & welcome" {
		t.Fatalf("preview %q", got)
	}
	if got := notificationText("x\u202ey\n\t z", 180); got != "xy z" {
		t.Fatalf("controls survived: %q", got)
	}
	if text := "برنامه‌ریزی 👩‍💻"; notificationText(text, 180) != text {
		t.Fatal("preview removed linguistic or emoji joiners")
	}
	if got := notificationText(strings.Repeat("界", 300), 180); len([]rune(got)) != 180 || !strings.HasSuffix(got, "…") {
		t.Fatalf("unbounded preview %q", got)
	}
}

func TestNotificationPreviewFetchesOnlyABoundedHTMLPart(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	server.SetDeadline(time.Now().Add(5 * time.Second))
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		fmt.Fprint(server, "* PREAUTH [CAPABILITY IMAP4rev1] Ready\r\n")
		r := bufio.NewReader(server)
		read := func(want string) (string, error) {
			line, err := r.ReadString('\n')
			if err != nil {
				return "", err
			}
			tag, command, _ := strings.Cut(line, " ")
			if !strings.Contains(command, want) {
				return "", fmt.Errorf("got %q, want %s", command, want)
			}
			if want == "BODYSTRUCTURE" && (!strings.Contains(command, "<0.4096>") || strings.Contains(command, "ENVELOPE")) {
				return "", fmt.Errorf("unbounded notification header fetch: %q", command)
			}
			return tag, nil
		}
		tag, err := read("SELECT")
		if err != nil {
			done <- err
			return
		}
		fmt.Fprintf(server, "* 1 EXISTS\r\n* OK [UIDVALIDITY 7] Valid\r\n%s OK Selected\r\n", tag)
		tag, err = read("BODYSTRUCTURE")
		if err != nil {
			done <- err
			return
		}
		// The declared part is huge; the preview must ask the wire for only 16 KiB.
		header := "Subject: Subject\r\n\r\n"
		fmt.Fprintf(server, "* 1 FETCH (UID 1 BODYSTRUCTURE (\"TEXT\" \"HTML\" (\"CHARSET\" \"UTF-8\") NIL NIL \"7BIT\" 10000000 10 NIL NIL NIL NIL) BODY[HEADER.FIELDS (SUBJECT)]<0> {%d}\r\n%s)\r\n%s OK Fetched\r\n", len(header), header, tag)
		tag, err = read("BODY.PEEK[1]<0.16384>")
		if err != nil {
			done <- err
			return
		}
		body := `<head><title>Hidden</title></head><p>Hello &amp; welcome</p><img src="https://tracker.invalid/x">` + strings.Repeat(" ", notificationBodyMax)
		body = body[:notificationBodyMax]
		fmt.Fprintf(server, "* 1 FETCH (UID 1 BODY[1]<0> {%d}\r\n%s)\r\n%s OK Fetched\r\n", len(body), body, tag)
		done <- nil
	}()
	c := imapclient.New(client, nil)
	defer c.Close()
	preview, err := notificationPreview(c, 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if preview != "Subject\nHello & welcome" {
		t.Fatalf("HTML-only preview: %q", preview)
	}
}
