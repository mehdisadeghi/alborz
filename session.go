package alborz

import (
	"context"
	"errors"
	"fmt"
	"github.com/fernet/fernet-go"
	"html/template"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
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

// Every bound alborz puts on an upstream, written once and named for
// what it bounds rather than for how long it is. Two numbers, because
// there are two kinds of waiting.
// TODO: make these configurable; how far away a server is belongs to
// the deployment, not to the binary.
const (
	// dialTimeout bounds opening the connection and nothing else: a host
	// that does not answer at all is wrong rather than far away, and the
	// login page is the visible casualty of waiting on it.
	dialTimeout = 3 * time.Second

	// RoundTripTimeout bounds one exchange that legitimately does work -
	// a step of a login handshake against a proxy that asks its own
	// backend before answering, a sieve command, an attachment fetch, a
	// body search with no server-side index. Spending one of these on
	// four handshake steps at once is what made a distant server look
	// like a broken one.
	RoundTripTimeout = 10 * time.Second

	// ScanTimeout bounds the one exchange the reader asked for knowing
	// it is slow: a whole-message search on a server without an index,
	// which reads every message of the folder.
	ScanTimeout = 2 * time.Minute

	// sessionDuration is how long a session outlives its last request.
	sessionDuration = 30 * time.Minute
)

// MaxAttachmentSize bounds what one session holds in attachments not yet
// sent, and what one upload may carry.
const MaxAttachmentSize = 32 << 20

var ErrAttachmentCacheSize = errors.New("Attachments on session exceed maximum file size")

// UpstreamError is a server that did not answer, as distinct from
// Alborz failing. A timeout is not a wrong password and must not read as
// one, and it is not an internal error either: nothing here is broken,
// the other end simply did not reply.
type UpstreamError struct {
	// Service is what did not answer, in the reader's terms: mail,
	// filters, calendar.
	Service string
	// After is how long was waited before giving up. Zero means the
	// connection was refused or the host could not be found, which is
	// not a wait at all and must not be described as one.
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

// BaselineError says the mail server lacks what everything here rests
// on. Every extension is used only where advertised and missed without
// harm; IMAP4rev1 or IMAP4rev2 is not an extension, and a server that
// offers neither is refused at the door rather than failing on the
// first page.
type BaselineError struct {
	Caps []string
}

func (err BaselineError) Error() string {
	return fmt.Sprintf("the mail server speaks neither IMAP4rev1 nor IMAP4rev2, only: %s", strings.Join(err.Caps, " "))
}

// AuthError wraps an authentication error.
type AuthError struct {
	cause error
}

func (err AuthError) Error() string {
	return fmt.Sprintf("authentication failed: %v", err.cause)
}

// Session is an active user session. It may also hold an IMAP connection.
//
// The session's password is not available to plugins. Plugins should use the
// session helpers to authenticate outgoing connections, for instance DoSMTP.
type Session struct {
	manager            *SessionManager
	username, password string
	domain             string
	closed             chan struct{}
	pings              chan struct{}
	store              Store

	httpLocker   sync.Mutex
	httpPassword string // protected by httpLocker
	httpLoaded   bool   // protected by httpLocker

	imapLocker imapQueue
	imapConn   *imapclient.Client // protected by imapLocker
	workLocker imapQueue
	workConn   *imapclient.Client // lazy connection for prefetches and scans

	sieveLocker sync.Mutex
	sieveConn   SieveClient // protected by locker, can be nil
}

type Attachment struct {
	File *multipart.FileHeader
	Form *multipart.Form
}

// Done closes when the session ends, so work started for it can stop
// with it.
func (s *Session) Done() <-chan struct{} { return s.closed }

// alive reports whether the session still holds its account: a reaped
// or signed-out one does not, and the bag drops it.
func (s *Session) alive() bool {
	select {
	case <-s.closed:
		return false
	default:
		return true
	}
}

// WatchIMAP opens a second connection to the account and hands it over.
// It exists for one caller: the watcher that sits in IDLE waiting for
// the server to say something arrived.
//
// A second connection rather than the session's own, because DoIMAP
// serialises: an IDLE on the shared connection would hold the lock for
// as long as it waited, which is to say forever, and every page of that
// account would stop answering.
//
// onChange is for what reshapes a listing, arrivals and expunges;
// onFlags for a flag set on one message, which a listing takes in
// place rather than being fetched again for it.
func (s *Session) WatchIMAP(onChange func(), onFlags func(seqNum uint32, flags []imap.Flag)) (*imapclient.Client, error) {
	c, err := s.manager.dialIMAPWatch(s.domain, &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { onChange() },
		Mailbox: func(*imapclient.UnilateralDataMailbox) { onChange() },
		// A flag set in another client arrives as an untagged FETCH, not
		// as EXISTS: without this, reading a message on a phone changed
		// nothing here. The message must be consumed or the connection's
		// reader stalls behind it.
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
	err = c.Login(s.username, s.password).Wait()
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

func (s *Session) ping() {
	// Non-blocking: once the expiry goroutine is gone, a send would
	// block its caller forever; a dropped ping is harmless.
	select {
	case s.pings <- struct{}{}:
	default:
	}
}

// Username returns the session's username.
func (s *Session) Username() string {
	return s.username
}

// Domain returns the domain part of the session's address, which selects the
// upstream servers.
func (s *Session) Domain() string {
	return s.domain
}

// DoIMAP executes an IMAP operation on this session. The IMAP client can only
// be used from inside f.
func (s *Session) DoIMAP(f func(*imapclient.Client) error) error {
	return s.DoIMAPWithin(RoundTripTimeout, f)
}

// DoIMAPWithin is DoIMAP with the bound named by the caller.
func (s *Session) DoIMAPWithin(bound time.Duration, f func(*imapclient.Client) error) error {
	return s.DoIMAPContext(context.Background(), bound, f)
}

// DoIMAPBackground keeps speculative reads off the interactive connection.
func (s *Session) DoIMAPBackground(f func(*imapclient.Client) error) error {
	return s.DoIMAPWork(context.Background(), IMAPBackground, RoundTripTimeout, f)
}

func (s *Session) DoIMAPContext(ctx context.Context, bound time.Duration, f func(*imapclient.Client) error) error {
	return s.DoIMAPWork(ctx, IMAPForeground, bound, f)
}

// IMAPClass separates scheduling from the operation's time budget.
type IMAPClass int

const (
	IMAPForeground IMAPClass = iota
	IMAPBackground           // speculative listings, bodies and ancillary facts
	IMAPScan                 // explicitly requested long reads, ahead of background batches
)

// waitError is what an IMAP wait that ended early comes to: the bound
// running out is the mail server's silence, and anything else - the
// session closing on a sign-out, the reader gone - is a cancellation,
// which says nothing about the server.
func waitError(err error, bound time.Duration) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return UpstreamError{Service: "mail", After: bound, cause: err}
	}
	return context.Canceled
}

func (s *Session) DoIMAPWork(ctx context.Context, class IMAPClass, bound time.Duration, f func(*imapclient.Client) error) (err error) {
	deadline := time.Now().Add(bound)
	waiting, stopWaiting := context.WithDeadline(ctx, deadline)
	defer stopWaiting()
	queue, conn := &s.imapLocker, &s.imapConn
	if class == IMAPBackground || class == IMAPScan {
		queue, conn = &s.workLocker, &s.workConn
	}
	start := time.Now()
	err = queue.acquire(waiting, s.Done(), class == IMAPBackground)
	AddTiming(ctx, "imap_queue", start)
	if err != nil {
		return waitError(err, bound)
	}
	defer queue.release()
	// The reader gone is theirs to hear; the bound run out in the queue
	// is the mail server's, said below as every other wait is.
	if err := ctx.Err(); err != nil {
		return err
	}
	if start, ok := ctx.Value(imapStartKey{}).(func() bool); ok && !start() {
		return context.Canceled
	}
	// Once a command starts it must be drained before this connection can
	// be reused. Browser cancellation only abandons the wait, not the wire.
	ctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	go func() {
		select {
		case <-s.Done():
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
		*conn, err = s.manager.connectIMAPContext(ctx, s.domain, s.username, s.password)
		if err != nil {
			var refused AuthError
			if errors.As(err, &refused) {
				s.Close()
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

// DoSMTP executes an SMTP operation on this session. The SMTP client can only
// be used from inside f.
func (s *Session) DoSMTP(f func(*smtp.Client) error) error {
	c, err := s.manager.dialSMTP(s.domain)
	if err != nil {
		return err
	}
	defer c.Close()

	auth := sasl.NewPlainClient("", s.username, s.password)
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

// DoSieve executes a ManageSieve operation on this session. The client can
// only be used from inside f. The connection is kept for later operations so
// the server doesn't have to re-authenticate every time.
func (s *Session) DoSieve(f func(SieveClient) error) error {
	s.sieveLocker.Lock()
	defer s.sieveLocker.Unlock()

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

	if s.sieveConn != nil {
		// The kept connection is used as is: probing it first would cost
		// every page a round trip to guard against the rare case that the
		// server dropped it, which the retry below handles anyway.
		err := run(s.sieveConn)
		if timedOut {
			s.sieveConn = nil
			return err
		}
		var serverErr *managesieve.ServerError
		if err == nil || errors.As(err, &serverErr) {
			// The server answered, so the connection is healthy.
			return err
		}
		// Anything else leaves the connection dead or out of sync; drop
		// it either way, but only a dropped connection warrants a silent
		// retry. A garbled exchange must surface, or a write could be
		// repeated on the server.
		s.sieveConn.Close()
		s.sieveConn = nil
		if !isSieveConnClosed(err) {
			return err
		}
	}

	c, err := s.manager.dialSieve(s.domain, s.username, s.password)
	if err != nil {
		return err
	}
	s.sieveConn = c

	err = run(s.sieveConn)
	if timedOut {
		s.sieveConn = nil
	}
	return err
}

// isSieveConnClosed reports whether the operation failed because the server
// had dropped the connection, the one error worth retrying on a fresh one. A
// script the server rejects must not be silently sent twice.
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

// SetHTTPBasicAuth adds an Authorization header field to the request
// with this session's credentials for HTTP services.
func (s *Session) SetHTTPBasicAuth(req *http.Request) error {
	password, err := s.HTTPPassword()
	if err != nil {
		return err
	}
	req.SetBasicAuth(s.username, password)
	return nil
}

// Close destroys the session. This can be used to log the user out.
func (s *Session) Close() {
	select {
	case <-s.closed:
		// This space is intentionally left blank
	default:
		close(s.closed)
	}
}

// Notice is what the next page tells the reader about the request
// that redirected to it: what happened, how it went, and what follows
// from it, when something does.
type Notice struct {
	Kind string
	Text string
	// Markup is the text with a link in it, for a notice that names a
	// place; the text is what the markup says, for the log.
	Markup template.HTML
	// Action is the one thing a reader may want next - to undo, to
	// delete the rest, to see what was sent. Nil when nothing follows.
	Action *NoticeAction
}

const (
	NoticeDone    = "done"
	NoticeWarning = "warning"
	NoticeFailed  = "failed"
)

// NoticeAction is a link, or a POST when it changes state: an undo is
// a request the page must not make by following a link.
type NoticeAction struct {
	Label  string
	Path   string
	Fields url.Values
}

// Undo is the action a notice carries when the handler knows the exact
// inverse of what it just did: a POST back to itself with what that
// needs, marked so the answer offers nothing further.
func (ctx *Context) Undo(path string, fields url.Values) *NoticeAction {
	fields.Set("undo", "1")
	return &NoticeAction{Label: ctx.T("notice.undo"), Path: path, Fields: fields}
}

// Unreachable says on the page which accounts' servers did not answer,
// so that a merged page can show what the others hold rather than
// nothing. Nothing is said when every account answered.
func (ctx *Context) Unreachable(accounts []string) {
	if len(accounts) == 0 {
		return
	}
	ctx.Notify(Notice{Kind: NoticeWarning,
		Text: ctx.Tf("notice.unreachable", len(accounts), strings.Join(accounts, ", "))})
}

// NoticeName is a thing's own name inside a notice: quoted by the
// sentence around it and cut by the page, since how much of a name
// fits is the screen's business and not this side's.
func NoticeName(name string) string {
	return `<span class="notice-name">` + template.HTMLEscapeString(name) + `</span>`
}

// NoticeLink is the same name, leading to the thing it names.
func NoticeLink(name, href string) string {
	return `<a class="notice-name" href="` + template.HTMLEscapeString(href) + `">` +
		template.HTMLEscapeString(name) + `</a>`
}

// Made is what every section says after making something. The reader
// lands on the list, which is where the new thing now lives, and the
// banner holds the two things wanted next: the thing itself, named and
// linked, and the offer to make another with the form as it was.
// sentence takes the name; again is the form's own address.
func (ctx *Context) Made(sentence, name, href, again string) {
	// A folder is its own list, so the reader is already looking at
	// what was made and there is nowhere for the name to lead.
	marked := NoticeName(name)
	if href != "" {
		marked = NoticeLink(name, href)
	}
	ctx.Notify(Notice{
		Kind:   NoticeDone,
		Text:   fmt.Sprintf(sentence, name),
		Markup: template.HTML(fmt.Sprintf(sentence, marked)),
		Action: &NoticeAction{Label: ctx.T("common.addanother"), Path: again},
	})
}

// Store returns a store suitable for storing persistent user data.
func (s *Session) Store() Store {
	return s.store
}

type (
	// DialIMAPFunc connects to the domain's upstream IMAP server.
	DialIMAPFunc func(domain string) (*imapclient.Client, error)
	// DialIMAPWatchFunc dials with a handler for the updates a server
	// sends unasked, which has to be given before the connection is
	// made.
	DialIMAPWatchFunc func(domain string, h *imapclient.UnilateralDataHandler) (*imapclient.Client, error)
	// DialSMTPFunc connects to the domain's upstream SMTP server.
	DialSMTPFunc func(domain string) (*smtp.Client, error)
)

// SessionManager keeps track of active sessions. It connects and re-connects
// to the upstream IMAP server as necessary. It prunes expired sessions.
type SessionManager struct {
	dialIMAP      DialIMAPFunc
	dialIMAPWatch DialIMAPWatchFunc
	dialSMTP      DialSMTPFunc
	dialSieve     DialSieveFunc
	logger        echo.Logger
	// loginKey seals what the store keeps for an account; nil leaves
	// only the password wrap.
	loginKey *fernet.Key
	// kept is where an account's settings go when its server has no
	// METADATA to hold them; nil where alborz keeps nothing itself.
	kept VisitRecords

	locker sync.Mutex
	// sessions are the ones still open, for the shutdown that closes
	// them. A session is reached through the visit that holds it, so
	// nothing looks anything up here.
	sessions map[*Session]struct{} // protected by locker
	// warnedTransientStore says the METADATA warning has been printed;
	// protected by locker, like the sessions it is set beside.
	warnedTransientStore bool
}

func newSessionManager(dialIMAP DialIMAPFunc, dialWatch DialIMAPWatchFunc, dialSMTP DialSMTPFunc, dialSieve DialSieveFunc, logger echo.Logger, loginKey *fernet.Key, kept VisitRecords) *SessionManager {
	return &SessionManager{
		kept:          kept,
		sessions:      make(map[*Session]struct{}),
		dialIMAP:      dialIMAP,
		dialIMAPWatch: dialWatch,
		dialSMTP:      dialSMTP,
		dialSieve:     dialSieve,
		logger:        logger,
		loginKey:      loginKey,
	}
}

func (sm *SessionManager) Close() {
	for s := range sm.sessions {
		s.Close()
	}
}

func (sm *SessionManager) connectIMAP(domain, username, password string) (*imapclient.Client, error) {
	return sm.connectIMAPContext(context.Background(), domain, username, password)
}

func (sm *SessionManager) connectIMAPContext(ctx context.Context, domain, username, password string) (*imapclient.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, RoundTripTimeout)
	defer cancel()
	c, err := sm.dialIMAP(domain)
	if err != nil {
		// A refused connection, an unresolvable host, a dial that timed
		// out: all of them are the server not answering, which is not
		// this program failing. A misconfigured domain is ours and
		// stays as it is.
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
		return nil, UpstreamError{Service: "mail", After: RoundTripTimeout, cause: ctx.Err()}
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
		return nil, BaselineError{Caps: names}
	}

	return c, nil
}

// Put connects to the IMAP server and creates a new session. If authentication
// fails, the error will be of type AuthError. Addresses outside the served
// domains are rejected with UnknownDomainError.
func (sm *SessionManager) Put(username, password string) (*Session, error) {
	_, domain, _ := strings.Cut(username, "@")
	c, err := sm.connectIMAP(domain, username, password)
	if err != nil {
		return nil, err
	}

	sm.locker.Lock()
	defer sm.locker.Unlock()

	s := &Session{
		manager:  sm,
		closed:   make(chan struct{}),
		pings:    make(chan struct{}, 5),
		imapConn: c,
		username: username,
		password: password,
		domain:   domain,
	}

	s.store, err = sm.newStore(s)
	if err != nil {
		return nil, err
	}

	sm.sessions[s] = struct{}{}

	go sm.reap(s)

	return s, nil
}

// reap waits out the session's life, keeping it alive on every ping,
// then closes its connections and forgets it.
func (sm *SessionManager) reap(s *Session) {
	timer := time.NewTimer(sessionDuration)

	alive := true
	for alive {
		select {
		case <-s.pings:
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(sessionDuration)
		case <-timer.C:
			alive = false
		case <-s.closed:
			alive = false
		}
	}

	timer.Stop()

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

	// An expired session is not an account gone: what the caches hold
	// for it is what the next sign-in, or the cookie restoring it, is
	// served from. Only signing out forgets, see Server.ForgetAccount.
	sm.locker.Lock()
	delete(sm.sessions, s)
	sm.locker.Unlock()
}
