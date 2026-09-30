package alborztest

import (
	"bytes"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// SMTPServer takes any credentials and every message, save for a recipient at refused@.
type SMTPServer struct {
	Addr     string
	mu       sync.Mutex
	all      []string
	sessions int
}

type SMTPOption func(*smtpConfig)

type smtpConfig struct{ hidden []string }

// Hide leaves the extensions out of the server's EHLO.
func Hide(extensions ...string) SMTPOption {
	return func(c *smtpConfig) { c.hidden = append(c.hidden, extensions...) }
}

func SMTP(t *testing.T, opts ...SMTPOption) *SMTPServer {
	t.Helper()
	var c smtpConfig
	for _, o := range opts {
		o(&c)
	}
	s := &SMTPServer{}
	srv := smtp.NewServer(s)
	srv.AllowInsecureAuth = true
	srv.Domain = "localhost"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ehloHiding{ln, c.hidden})
	t.Cleanup(func() { srv.Close() })
	s.Addr = ln.Addr().String()
	return s
}

func (s *SMTPServer) Upstream() string { return "smtp+insecure://" + s.Addr }

// Sessions counts the connections the server took.
func (s *SMTPServer) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions
}

// Last is the last message sent, as it went over the wire, or "".
func (s *SMTPServer) Last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.all) == 0 {
		return ""
	}
	return s.all[len(s.all)-1]
}

// All are the messages sent, in order.
func (s *SMTPServer) All() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.all)
}

func (s *SMTPServer) NewSession(*smtp.Conn) (smtp.Session, error) {
	s.mu.Lock()
	s.sessions++
	s.mu.Unlock()
	return smtpSession{s}, nil
}

type smtpSession struct{ s *SMTPServer }

func (smtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (smtpSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(string, string, string) error { return nil }), nil
}

func (smtpSession) Mail(string, *smtp.MailOptions) error { return nil }

func (smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	if strings.HasPrefix(to, "refused@") {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "Recipient address rejected: User unknown"}
	}
	return nil
}

func (smtpSession) Reset()        {}
func (smtpSession) Logout() error { return nil }

func (ss smtpSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	ss.s.mu.Lock()
	ss.s.all = append(ss.s.all, string(b))
	ss.s.mu.Unlock()
	return nil
}

type ehloHiding struct {
	net.Listener
	hidden []string
}

func (l ehloHiding) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	return ehloHidingConn{c, l.hidden}, err
}

type ehloHidingConn struct {
	net.Conn
	hidden []string
}

func (c ehloHidingConn) Write(b []byte) (int, error) {
	out := b
	for _, ext := range c.hidden {
		out = bytes.ReplaceAll(out, []byte("250-"+ext+"\r\n"), nil)
	}
	if _, err := c.Conn.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}
