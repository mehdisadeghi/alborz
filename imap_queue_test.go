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
	s := &Login{imapConn: client, closed: make(chan struct{})}
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

func waitQueued(t *testing.T, q *imapQueue, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		q.mu.Lock()
		queued := len(q.waiters)
		q.mu.Unlock()
		if queued == n {
			return
		}
	}
	t.Fatalf("%d waiters never queued", n)
}

func TestMailQueueFullRefuses(t *testing.T) {
	l := &Login{closed: make(chan struct{})}
	q, done := &l.imapLocker, l.Done()
	if err := q.acquire(context.Background(), done, false); err != nil {
		t.Fatal(err)
	}
	granted := make(chan error, imapQueueLimit)
	for range imapQueueLimit {
		go func() {
			err := q.acquire(context.Background(), done, false)
			granted <- err
			if err == nil {
				q.release()
			}
		}()
	}
	waitQueued(t, q, imapQueueLimit)
	err := l.DoIMAPWork(context.Background(), IMAPForeground, time.Second, func(*imapclient.Client) error { return nil })
	if !errors.Is(err, errIMAPQueueFull) {
		t.Fatalf("request past a full queue: %v", err)
	}
	q.release()
	for range imapQueueLimit {
		if err := <-granted; err != nil {
			t.Fatalf("queued request: %v", err)
		}
	}
}

func TestScanRunsBeforeBackground(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := imapclient.New(clientConn, nil)
	defer client.Close()
	l := &Login{workConn: client, closed: make(chan struct{})}
	hold := make(chan struct{})
	held := make(chan struct{})
	go l.DoIMAPWork(context.Background(), IMAPBackground, time.Minute, func(*imapclient.Client) error {
		close(held)
		<-hold
		return nil
	})
	<-held
	ran := make(chan IMAPClass, 2)
	for i, class := range []IMAPClass{IMAPBackground, IMAPScan} {
		go l.DoIMAPWork(context.Background(), class, time.Minute, func(*imapclient.Client) error {
			ran <- class
			return nil
		})
		waitQueued(t, &l.workLocker, i+1)
	}
	close(hold)
	if first, second := <-ran, <-ran; first != IMAPScan || second != IMAPBackground {
		t.Fatalf("ran %v then %v, want the scan first", first, second)
	}
}

func TestMailQueueGrantRacingCancel(t *testing.T) {
	var q imapQueue
	for range 1000 {
		if err := q.acquire(context.Background(), nil, false); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error)
		go func() { result <- q.acquire(ctx, nil, false) }()
		waitQueued(t, &q, 1)
		cancel()
		q.release()
		if err := <-result; err == nil {
			q.release()
		}
		free, stop := context.WithTimeout(context.Background(), time.Second)
		err := q.acquire(free, nil, false)
		stop()
		if err != nil {
			t.Fatalf("the slot leaked: %v", err)
		}
		q.release()
	}
}
