package alborz

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/emersion/go-imap/v2/imapclient"
	"net"
	"strings"
	"testing"
	"time"
)

func TestMailQueueCancellation(t *testing.T) {
	var q imapQueue
	if err := q.acquire(context.Background(), nil, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := q.acquire(ctx, nil, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued request: %v", err)
	}
	q.release()
	if err := q.acquire(context.Background(), nil, false); err != nil {
		t.Fatal(err)
	}
	q.release()
}

func TestActiveIMAPSurvivesReaderCancellation(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := imapclient.New(clientConn, nil)
	defer client.Close()
	go func() {
		fmt.Fprint(serverConn, "* PREAUTH [CAPABILITY IMAP4rev1] ready\r\n")
		scanner := bufio.NewScanner(serverConn)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 2 {
				fmt.Fprintf(serverConn, "%s OK completed\r\n", fields[0])
			}
		}
	}()
	if err := client.WaitGreeting(); err != nil {
		t.Fatal(err)
	}
	s := &Session{imapConn: client, closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := s.DoIMAPWork(ctx, IMAPForeground, time.Second, func(c *imapclient.Client) error {
		cancel()
		return c.Noop().Wait()
	})
	if err != nil {
		t.Fatalf("cancelled reader interrupted active command: %v", err)
	}
	err = s.DoIMAPWork(context.Background(), IMAPForeground, time.Second, func(c *imapclient.Client) error {
		if c != client {
			t.Error("the next request reconnected")
		}
		return c.Noop().Wait()
	})
	if err != nil {
		t.Fatalf("connection was not reusable: %v", err)
	}
}
