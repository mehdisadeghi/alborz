package alborz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/labstack/echo/v4"
	"go.guido-berhoerster.org/managesieve"
)

// TODO: make these configurable per deployment.
const (
	// dialTimeout is short: a host that does not answer at all is misconfigured, not far away.
	dialTimeout = 3 * time.Second

	// RoundTripTimeout bounds one exchange that legitimately does work.
	RoundTripTimeout = 10 * time.Second

	// signInTimeout is longer because a server may slow its refusals to a guessing sign-in.
	signInTimeout = 20 * time.Second

	// ScanTimeout bounds a whole-folder search on a server without an index.
	ScanTimeout = 2 * time.Minute

	// LoginDuration is how long a login outlives its last request.
	LoginDuration = 30 * time.Minute
)

// UpstreamError is a server that did not answer, as distinct from alborz failing.
type UpstreamError struct {
	Service string
	// After zero means the connection was refused or the host not found.
	After time.Duration
	cause error
}

func (err UpstreamError) Error() string {
	if err.After == 0 {
		return fmt.Sprintf("could not reach the %s server: %v", err.Service, err.cause)
	}
	return fmt.Sprintf("%s server did not answer within %v: %v", err.Service, err.After, err.cause)
}

func (err UpstreamError) Unwrap() error { return err.cause }

// BaselineError says the mail server speaks neither IMAP4rev1 nor IMAP4rev2.
type BaselineError struct {
	caps []string
}

func (err BaselineError) Error() string {
	return fmt.Sprintf("the mail server speaks neither IMAP4rev1 nor IMAP4rev2, only: %s", strings.Join(err.caps, " "))
}

// AuthError wraps an authentication error.
type AuthError struct {
	cause error
}

func (err AuthError) Error() string {
	return fmt.Sprintf("authentication failed: %v", err.cause)
}

// Login is an account signed in.
type Login struct {
	manager            *loginManager
	username, password string
	domain             string
	closed             chan struct{}
	pings              chan struct{}
	store              Store

	// httpPassword is cached because opening it costs an Argon2id derivation.
	httpLocker   sync.Mutex
	httpPassword string // protected by httpLocker
	httpLoaded   bool   // protected by httpLocker
	httpSealed   bool   // protected by httpLocker

	imapLocker imapQueue
	imapConn   *imapclient.Client // protected by imapLocker
	workLocker imapQueue
	workConn   *imapclient.Client
	transfers  chan idleTransfer

	sieveLocker sync.Mutex
	sieveConn   SieveClient // protected by sieveLocker, can be nil
}

// Done closes when the login ends.
func (l *Login) Done() <-chan struct{} { return l.closed }

func (l *Login) Alive() bool {
	select {
	case <-l.closed:
		return false
	default:
		return true
	}
}

// WatchIMAP opens a second connection for IDLE, since DoIMAP serialises and
// an IDLE on the shared connection would block every page of the account.
func (l *Login) WatchIMAP(onChange func(), onFlags func(seqNum uint32, flags []imap.Flag), onFolder func(mailbox string), onOverflow func()) (*imapclient.Client, error) {
	c, err := l.manager.dialIMAPWatch(l.domain, &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { onChange() },
		Mailbox: func(*imapclient.UnilateralDataMailbox) { onChange() },
		Status: func(data *imap.StatusData) {
			if data != nil && data.Mailbox != "" {
				onFolder(data.Mailbox)
			}
		},
		NotificationOverflow: onOverflow,
		// A flag set in another client arrives as an untagged FETCH; it must be
		// consumed or the connection's reader stalls.
		Fetch: func(msg *imapclient.FetchMessageData) {
			buf, err := msg.Collect()
			if err != nil || buf.Flags == nil {
				onChange()
				return
			}
			onFlags(buf.SeqNum, buf.Flags)
		},
	})
	if err != nil {
		return nil, err
	}
	watchdog := time.AfterFunc(RoundTripTimeout, func() { c.Close() })
	err = c.Login(l.username, l.password).Wait()
	timedOut := !watchdog.Stop()
	if err != nil || timedOut {
		c.Close()
		if timedOut {
			return nil, UpstreamError{Service: "mail", After: RoundTripTimeout, cause: err}
		}
		return nil, AuthError{err}
	}
	return c, nil
}

func (l *Login) Ping() {
	// Non-blocking: once the expiry goroutine is gone, a send would block forever.
	select {
	case l.pings <- struct{}{}:
	default:
	}
}

// Username returns the login's username.
func (l *Login) Username() string {
	return l.username
}

// Domain returns the domain part of the login's address.
func (l *Login) Domain() string {
	return l.domain
}

// DoIMAP executes an IMAP operation on this login; the client is valid only inside f.
func (l *Login) DoIMAP(f func(*imapclient.Client) error) error {
	return l.doIMAPWithin(RoundTripTimeout, f)
}

// doIMAPWithin is DoIMAP with the caller's bound.
func (l *Login) doIMAPWithin(bound time.Duration, f func(*imapclient.Client) error) error {
	return l.DoIMAPContext(context.Background(), bound, f)
}

// DoIMAPBackground keeps speculative reads off the interactive connection.
func (l *Login) DoIMAPBackground(f func(*imapclient.Client) error) error {
	return l.DoIMAPWork(context.Background(), IMAPBackground, RoundTripTimeout, f)
}

func (l *Login) DoIMAPContext(ctx context.Context, bound time.Duration, f func(*imapclient.Client) error) error {
	return l.DoIMAPWork(ctx, IMAPForeground, bound, f)
}

// IMAPClass separates scheduling from the operation's time budget.
type IMAPClass int

const (
	IMAPForeground IMAPClass = iota
	IMAPBackground
	IMAPScan
)

func waitError(err error, bound time.Duration) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return UpstreamError{Service: "mail", After: bound, cause: err}
	case errors.Is(err, ErrIMAPQueueFull):
		return err
	}
	return context.Canceled
}

func (l *Login) DoIMAPWork(ctx context.Context, class IMAPClass, bound time.Duration, f func(*imapclient.Client) error) (err error) {
	deadline := time.Now().Add(bound)
	waiting, stopWaiting := context.WithDeadline(ctx, deadline)
	defer stopWaiting()
	queue, conn := &l.imapLocker, &l.imapConn
	if class == IMAPBackground || class == IMAPScan {
		queue, conn = &l.workLocker, &l.workConn
	}
	start := time.Now()
	err = queue.acquire(waiting, l.Done(), class == IMAPBackground)
	AddTiming(ctx, "imap_queue", start)
	if err != nil {
		return waitError(err, bound)
	}
	defer queue.release()
	// A gone reader is a cancellation; a queue timeout is reported as the server's.
	if err := ctx.Err(); err != nil {
		return err
	}
	if start, ok := ctx.Value(imapStartKey{}).(func() bool); ok && !start() {
		return context.Canceled
	}
	// A started command must be drained before the connection is reused.
	ctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	go func() {
		select {
		case <-l.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	if err := ctx.Err(); err != nil {
		return waitError(err, bound)
	}
	start = time.Now()
	defer func() { AddTiming(ctx, "imap_work", start) }()
	if *conn != nil && (*conn).State() == imap.ConnStateLogout {
		(*conn).Close()
		*conn = nil
	}
	if *conn == nil {
		*conn, err = l.manager.connectIMAPContext(ctx, l.manager.dialIMAP, l.domain, l.username, l.password, "")
		if err != nil {
			var refused AuthError
			if errors.As(err, &refused) {
				l.Close()
			}
			return err
		}
	}
	c := *conn
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		c.Close()
		close(stopped)
	})
	defer func() {
		if !stop() {
			<-stopped
			*conn = nil
			err = waitError(ctx.Err(), bound)
		}
	}()
	return f(c)
}

// maxIMAPTransfers bounds a login's downloads at once: Dovecot allows ten
// connections per user and address by default, and the login's two lanes and
// watcher take three of them.
const maxIMAPTransfers = 2

// DoIMAPTransfer runs a download on a connection of its own, so it neither
// waits behind the account's pages nor holds them up however long it takes.
// It ends with ctx, or when the server falls silent.
func (l *Login) DoIMAPTransfer(ctx context.Context, f func(*imapclient.Client) error) error {
	var slot idleTransfer
	select {
	case slot = <-l.transfers:
	case <-ctx.Done():
		return ctx.Err()
	case <-l.Done():
		return context.Canceled
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-l.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	c, keep := slot.c, false
	defer func() {
		if keep {
			l.transfers <- idleTransfer{c, time.Now()}
			return
		}
		if c != nil {
			c.Close()
		}
		l.transfers <- idleTransfer{}
	}()
	// Silence closes an idle connection, so one close to that is not taken.
	if c != nil && (c.State() == imap.ConnStateLogout || time.Since(slot.since) > transferStall/2) {
		c.Close()
		c = nil
	}
	if c == nil {
		var err error
		if c, err = l.manager.connectIMAPContext(ctx, l.manager.dialIMAPTransfer, l.domain, l.username, l.password, ""); err != nil {
			return err
		}
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	err := f(c)
	keep = stop() && err == nil
	return err
}

// idleTransfer is a transfer's slot, holding the connection it left for the
// next, as a page's images follow one another; c is nil when none was left.
type idleTransfer struct {
	c     *imapclient.Client
	since time.Time
}

// DoSMTP executes an SMTP operation on this login; the client is valid only inside f.
func (l *Login) DoSMTP(f func(*smtp.Client) error) error {
	c, err := l.manager.dialSMTP(l.domain)
	if err != nil {
		return err
	}
	defer c.Close()

	auth := sasl.NewPlainClient("", l.username, l.password)
	if err := c.Auth(auth); err != nil {
		return AuthError{err}
	}

	if err := f(c); err != nil {
		return err
	}

	if err := c.Quit(); err != nil {
		return fmt.Errorf("QUIT failed: %v", err)
	}

	return nil
}

// DoSieve executes a ManageSieve operation on this login, reusing the connection.
func (l *Login) DoSieve(f func(SieveClient) error) error {
	l.sieveLocker.Lock()
	defer l.sieveLocker.Unlock()

	timedOut := false
	run := func(c SieveClient) error {
		watchdog := time.AfterFunc(RoundTripTimeout, func() { c.Close() })
		err := f(c)
		if !watchdog.Stop() {
			timedOut = true
			return fmt.Errorf("sieve command timed out after %v", RoundTripTimeout)
		}
		return err
	}

	if l.sieveConn != nil {
		// Used without probing; a dropped connection is retried below.
		err := run(l.sieveConn)
		if timedOut {
			l.sieveConn = nil
			return err
		}
		var serverErr *managesieve.ServerError
		if err == nil || errors.As(err, &serverErr) {
			return err
		}
		// Only a dropped connection is retried silently, or a write could repeat.
		l.sieveConn.Close()
		l.sieveConn = nil
		if !isSieveConnClosed(err) {
			return err
		}
	}

	c, err := l.manager.dialSieve(l.domain, l.username, l.password)
	if err != nil {
		return err
	}
	l.sieveConn = c

	err = run(l.sieveConn)
	if timedOut {
		l.sieveConn = nil
	}
	return err
}

// isSieveConnClosed reports whether the server dropped the connection, the one
// error safe to retry.
func isSieveConnClosed(err error) bool {
	if err == nil {
		return false
	}
	var closed *managesieve.ConnClosedError
	if errors.As(err, &closed) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET)
}

// SetMailBasicAuth sets the mail login's credentials on req, for a server on
// the mail host that shares its accounts (ADR 28).
func (l *Login) SetMailBasicAuth(req *http.Request) {
	req.SetBasicAuth(l.username, l.password)
}

// Close ends the login.
func (l *Login) Close() {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
}

// Store returns the account's settings store.
func (l *Login) Store() Store {
	return l.store
}

type (
	// dialIMAPFunc connects to the domain's IMAP server, for a reader at from,
	// or for alborz itself when from is empty.
	dialIMAPFunc func(domain, from string) (*imapclient.Client, error)
	// dialIMAPWatchFunc dials with a handler for unsolicited updates.
	dialIMAPWatchFunc func(domain string, h *imapclient.UnilateralDataHandler) (*imapclient.Client, error)
	// dialSMTPFunc connects to the domain's upstream SMTP server.
	dialSMTPFunc func(domain string) (*smtp.Client, error)
)

// loginManager keeps track of active logins and prunes expired ones.
type loginManager struct {
	dialIMAP         dialIMAPFunc
	dialIMAPTransfer dialIMAPFunc
	dialIMAPWatch    dialIMAPWatchFunc
	dialSMTP         dialSMTPFunc
	dialSieve        dialSieveFunc
	logger           echo.Logger
	// settings holds settings for servers without METADATA; nil if alborz keeps none.
	settings *settingsRecords

	locker sync.Mutex
	// logins are the open ones, for shutdown to close.
	logins           map[*Login]struct{} // protected by locker
	warnedNoMetadata bool                // protected by locker
}

func newLoginManager(dialIMAP, dialTransfer dialIMAPFunc, dialWatch dialIMAPWatchFunc, dialSMTP dialSMTPFunc, dialSieve dialSieveFunc, logger echo.Logger, settings *settingsRecords) *loginManager {
	return &loginManager{
		settings:         settings,
		logins:           make(map[*Login]struct{}),
		dialIMAP:         dialIMAP,
		dialIMAPTransfer: dialTransfer,
		dialIMAPWatch:    dialWatch,
		dialSMTP:         dialSMTP,
		dialSieve:        dialSieve,
		logger:           logger,
	}
}

func (sm *loginManager) close() {
	sm.locker.Lock()
	defer sm.locker.Unlock()
	for s := range sm.logins {
		s.Close()
	}
}

func (sm *loginManager) connectIMAP(domain, username, password, from string) (*imapclient.Client, error) {
	return sm.connectIMAPContext(context.Background(), sm.dialIMAP, domain, username, password, from)
}

func (sm *loginManager) connectIMAPContext(ctx context.Context, dial dialIMAPFunc, domain, username, password, from string) (*imapclient.Client, error) {
	within := RoundTripTimeout
	if from != "" {
		within = signInTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	c, err := dial(domain, from)
	if err != nil {
		// A misconfigured domain is ours; any other dial failure is the server's.
		var unknown UnknownDomainError
		if errors.As(err, &unknown) {
			return nil, err
		}
		return nil, UpstreamError{Service: "mail", cause: err}
	}

	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { c.Close(); close(stopped) })
	err = c.Login(username, password).Wait()
	if !stop() {
		<-stopped
		return nil, UpstreamError{Service: "mail", After: within, cause: ctx.Err()}
	}
	if err != nil {
		c.Close()
		var status *imap.Error
		if errors.As(err, &status) {
			return nil, AuthError{err}
		}
		return nil, UpstreamError{Service: "mail", cause: err}
	}
	if caps := c.Caps(); !caps.Has(imap.CapIMAP4rev1) && !caps.Has(imap.CapIMAP4rev2) {
		c.Logout()
		names := make([]string, 0, len(caps))
		for cap := range caps {
			names = append(names, string(cap))
		}
		slices.Sort(names)
		return nil, BaselineError{caps: names}
	}

	return c, nil
}

// put connects to the IMAP server and creates a new login. It fails with
// AuthError or unknownDomainError.
func (sm *loginManager) put(username, password, from string) (*Login, error) {
	_, domain, _ := strings.Cut(username, "@")
	c, err := sm.connectIMAP(domain, username, password, from)
	if err != nil {
		return nil, err
	}

	sm.locker.Lock()
	defer sm.locker.Unlock()

	s := &Login{
		manager:   sm,
		closed:    make(chan struct{}),
		pings:     make(chan struct{}, 5),
		imapConn:  c,
		transfers: make(chan idleTransfer, maxIMAPTransfers),
		username:  username,
		password:  password,
		domain:    domain,
	}

	for range maxIMAPTransfers {
		s.transfers <- idleTransfer{}
	}

	s.store, err = sm.newStore(s)
	if err != nil {
		return nil, err
	}

	sm.logins[s] = struct{}{}

	go sm.reap(s)

	return s, nil
}

func (sm *loginManager) reap(s *Login) {
	timer := time.NewTimer(LoginDuration)

	alive := true
	for alive {
		select {
		case <-s.pings:
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(LoginDuration)
		case <-timer.C:
			alive = false
		case <-s.closed:
			alive = false
		}
	}

	timer.Stop()
	s.Close()

	s.imapLocker.acquire(context.Background(), nil, false)
	if s.imapConn != nil {
		s.imapConn.Close()
	}
	s.imapLocker.release()
	s.workLocker.acquire(context.Background(), nil, false)
	if s.workConn != nil {
		s.workConn.Close()
	}
	s.workLocker.release()

	s.sieveLocker.Lock()
	if s.sieveConn != nil {
		s.sieveConn.Close()
	}
	s.sieveLocker.Unlock()

	// A running transfer ends with the login; only idle ones are left to close.
	for len(s.transfers) > 0 {
		if t := <-s.transfers; t.c != nil {
			t.c.Close()
		}
	}

	// Caches outlive expiry; only signing out forgets (Server.ForgetAccount).
	sm.locker.Lock()
	delete(sm.logins, s)
	sm.locker.Unlock()
}
