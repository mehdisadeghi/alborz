package alborz

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/google/uuid"
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

	// roundTripTimeout bounds one exchange that legitimately does work -
	// a step of a login handshake against a proxy that asks its own
	// backend before answering, a sieve command, an attachment fetch, a
	// body search with no server-side index. Spending one of these on
	// four handshake steps at once is what made a distant server look
	// like a broken one.
	roundTripTimeout = 10 * time.Second

	// sessionDuration is how long a session outlives its last request.
	sessionDuration = 30 * time.Minute
)

const maxAttachmentSize = 32 << 20 // 32 MiB

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

var (
	ErrSessionExpired      = errors.New("session expired")
	ErrAttachmentCacheSize = errors.New("Attachments on session exceed maximum file size")
)

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
	token              string
	closed             chan struct{}
	pings              chan struct{}
	store              Store

	noticeLocker sync.Mutex
	notice       string // protected by noticeLocker

	imapLocker sync.Mutex
	imapConn   *imapclient.Client // protected by locker, can be nil

	sieveLocker sync.Mutex
	sieveConn   SieveClient // protected by locker, can be nil

	attachmentsLocker sync.Mutex
	attachments       map[string]*Attachment // protected by attachmentsLocker
}

type Attachment struct {
	File *multipart.FileHeader
	Form *multipart.Form
}

// Done closes when the session ends, so work started for it can stop
// with it.
func (s *Session) Done() <-chan struct{} { return s.closed }

// WatchIMAP opens a second connection to the account and hands it over.
// It exists for one caller: the watcher that sits in IDLE waiting for
// the server to say something arrived.
//
// A second connection rather than the session's own, because DoIMAP
// serialises: an IDLE on the shared connection would hold the lock for
// as long as it waited, which is to say forever, and every page of that
// account would stop answering.
func (s *Session) WatchIMAP(onChange func()) (*imapclient.Client, error) {
	c, err := s.manager.dialIMAPWatch(s.domain, &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { onChange() },
		Mailbox: func(*imapclient.UnilateralDataMailbox) { onChange() },
		// A flag set in another client arrives as an untagged FETCH, not
		// as EXISTS: without this, reading a message on a phone changed
		// nothing here. The message must be consumed or the connection's
		// reader stalls behind it.
		Fetch: func(msg *imapclient.FetchMessageData) {
			msg.Collect()
			onChange()
		},
	})
	if err != nil {
		return nil, err
	}
	watchdog := time.AfterFunc(roundTripTimeout, func() { c.Close() })
	err = c.Login(s.username, s.password).Wait()
	timedOut := !watchdog.Stop()
	if err != nil || timedOut {
		c.Close()
		if timedOut {
			return nil, fmt.Errorf("IMAP login timed out after %v", roundTripTimeout)
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
	s.imapLocker.Lock()
	defer s.imapLocker.Unlock()

	if s.imapConn != nil && s.imapConn.State() == imap.ConnStateLogout {
		s.imapConn.Close()
		s.imapConn = nil
	}

	if s.imapConn == nil {
		var err error
		s.imapConn, err = s.manager.connectIMAP(s.domain, s.username, s.password)
		if err != nil {
			s.Close()
			return fmt.Errorf("failed to re-connect to IMAP server: %w", err)
		}
	}

	// TODO: to avoid races wrt. disconnection, re-run f if it returns
	// io.UnexpectedEOF
	c := s.imapConn
	watchdog := time.AfterFunc(roundTripTimeout, func() { c.Close() })
	err := f(c)
	if !watchdog.Stop() {
		s.imapConn = nil
		return fmt.Errorf("IMAP command timed out after %v", roundTripTimeout)
	}
	return err
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
		watchdog := time.AfterFunc(roundTripTimeout, func() { c.Close() })
		err := f(c)
		if !watchdog.Stop() {
			timedOut = true
			return fmt.Errorf("sieve command timed out after %v", roundTripTimeout)
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

// SetHTTPBasicAuth adds an Authorization header field to the request with
// this session's credentials.
func (s *Session) SetHTTPBasicAuth(req *http.Request) {
	// TODO: find a way to make it harder for plugins to steal credentials
	req.SetBasicAuth(s.username, s.password)
}

// Close destroys the session. This can be used to log the user out.
func (s *Session) Close() {
	s.attachmentsLocker.Lock()
	defer s.attachmentsLocker.Unlock()

	for _, f := range s.attachments {
		f.Form.RemoveAll()
	}

	select {
	case <-s.closed:
		// This space is intentionally left blank
	default:
		close(s.closed)
	}
}

// Puts an attachment and returns a generated UUID
func (s *Session) PutAttachment(in *multipart.FileHeader,
	form *multipart.Form) (string, error) {
	id := uuid.New()
	s.attachmentsLocker.Lock()

	var size int64
	for _, a := range s.attachments {
		size += a.File.Size
	}
	if size+in.Size > maxAttachmentSize {
		return "", ErrAttachmentCacheSize
	}

	s.attachments[id.String()] = &Attachment{
		File: in,
		Form: form,
	}
	s.attachmentsLocker.Unlock()
	return id.String(), nil
}

// Removes an attachment from the session. Returns nil if there was no such
// attachment.
func (s *Session) PopAttachment(uuid string) *Attachment {
	s.attachmentsLocker.Lock()
	defer s.attachmentsLocker.Unlock()

	a, ok := s.attachments[uuid]
	if !ok {
		return nil
	}
	delete(s.attachments, uuid)

	return a
}

func (s *Session) PutNotice(n string) {
	s.noticeLocker.Lock()
	s.notice = n
	s.noticeLocker.Unlock()
}

func (s *Session) PopNotice() string {
	s.noticeLocker.Lock()
	defer s.noticeLocker.Unlock()
	n := s.notice
	s.notice = ""
	return n
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

	locker   sync.Mutex
	sessions map[string]*Session // protected by locker
}

func newSessionManager(dialIMAP DialIMAPFunc, dialWatch DialIMAPWatchFunc, dialSMTP DialSMTPFunc, dialSieve DialSieveFunc, logger echo.Logger) *SessionManager {
	return &SessionManager{
		sessions:      make(map[string]*Session),
		dialIMAP:      dialIMAP,
		dialIMAPWatch: dialWatch,
		dialSMTP:      dialSMTP,
		dialSieve:     dialSieve,
		logger:        logger,
	}
}

func (sm *SessionManager) Close() {
	for _, s := range sm.sessions {
		s.Close()
	}
}

func (sm *SessionManager) connectIMAP(domain, username, password string) (*imapclient.Client, error) {
	c, err := sm.dialIMAP(domain)
	if err != nil {
		return nil, err
	}

	watchdog := time.AfterFunc(roundTripTimeout, func() { c.Close() })
	err = c.Login(username, password).Wait()
	timedOut := !watchdog.Stop()
	if err != nil {
		c.Logout()
		if timedOut {
			return nil, fmt.Errorf("IMAP login timed out after %v", roundTripTimeout)
		}
		return nil, AuthError{err}
	}

	return c, nil
}

func (sm *SessionManager) get(token string) (*Session, error) {
	sm.locker.Lock()
	defer sm.locker.Unlock()

	session, ok := sm.sessions[token]
	if !ok {
		return nil, ErrSessionExpired
	}
	return session, nil
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

	var token string
	for {
		token, err = generateToken()
		if err != nil {
			c.Logout()
			return nil, err
		}

		if _, ok := sm.sessions[token]; !ok {
			break
		}
	}

	s := &Session{
		manager:     sm,
		closed:      make(chan struct{}),
		pings:       make(chan struct{}, 5),
		imapConn:    c,
		username:    username,
		password:    password,
		domain:      domain,
		token:       token,
		attachments: make(map[string]*Attachment),
	}

	s.store, err = newStore(s, sm.logger)
	if err != nil {
		return nil, err
	}

	sm.sessions[token] = s

	go func() {
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

		s.imapLocker.Lock()
		if s.imapConn != nil {
			s.imapConn.Close()
		}
		s.imapLocker.Unlock()

		s.sieveLocker.Lock()
		if s.sieveConn != nil {
			s.sieveConn.Close()
		}
		s.sieveLocker.Unlock()

		sm.locker.Lock()
		delete(sm.sessions, token)
		sm.locker.Unlock()
	}()

	return s, nil
}
