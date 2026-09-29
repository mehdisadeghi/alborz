package alborz

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"net"
	"os"
	"time"

	"github.com/cention-sany/utf7"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
)

// go-message's charset lookup does not know UTF-7 (RFC 2152).
func init() {
	for _, name := range []string{"utf-7", "unicode-1-1-utf-7"} {
		charset.RegisterEncoding(name, utf7.UTF7)
	}
}
func (s *Server) dialIMAP(domain, from string) (*imapclient.Client, error) {
	return s.dial(domain, from, nil, 0)
}

// dialIMAPTransfer dials a connection that waits on the server's silence, not
// on each response, so a download lasts as long as bytes keep coming.
func (s *Server) dialIMAPTransfer(domain, from string) (*imapclient.Client, error) {
	return s.dial(domain, from, nil, transferStall)
}

// transferStall is as long as go-imap waits for any answer; its bound on a
// whole literal would cut a large download on a slow link.
const transferStall = 30 * time.Second

// dialIMAPWatch dials with a handler for unsolicited updates; only a watcher
// passes one, or it would run on whichever request reads the socket.
func (s *Server) dialIMAPWatch(domain string, unilateral *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	return s.dial(domain, "", unilateral, 0)
}

func (s *Server) dial(domain, from string, unilateral *imapclient.UnilateralDataHandler, stall time.Duration) (*imapclient.Client, error) {
	d, ok := s.upstreamsFor(domain)
	if !ok {
		return nil, unknownDomainError{domain}
	}

	var debugWriter io.Writer
	if s.Options.Debug {
		debugWriter = os.Stderr
	}

	options := &imapclient.Options{
		DebugWriter: debugWriter,
		WordDecoder: &mime.WordDecoder{
			CharsetReader: charset.Reader,
		},
		UnilateralDataHandler: unilateral,
	}

	conn, err := net.DialTimeout("tcp", d.imap.host, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
	}
	if stall > 0 {
		conn = stallConn{Conn: conn, stall: stall}
	}
	host, _, _ := net.SplitHostPort(d.imap.host)
	var c *imapclient.Client
	if d.imap.tls {
		secure := tls.Client(conn, &tls.Config{ServerName: host, NextProtos: []string{"imap"}})
		ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		defer cancel()
		if err := secure.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to connect to IMAPS server: %w", err)
		}
		c = imapclient.New(secure, options)
	} else if !d.imap.insecure {
		options.TLSConfig = &tls.Config{ServerName: host}
		c, err = imapclient.NewStartTLS(conn, options)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
		}
	} else {
		c = imapclient.New(conn, options)
	}

	// Not awaited, so ID never holds up the sign-in; a server without ID answers BAD.
	if ip := net.ParseIP(from); ip != nil {
		c.ID(&imap.IDData{Raw: map[string]string{forwardedFor: ip.String()}})
	}
	return c, nil
}

// stallConn gives up on a server that sends nothing for stall.
type stallConn struct {
	net.Conn
	stall time.Duration
}

func (c stallConn) Read(b []byte) (int, error) {
	c.Conn.SetReadDeadline(time.Now().Add(c.stall))
	return c.Conn.Read(b)
}

// The client's deadlines count from the start of a response, however large.
func (stallConn) SetReadDeadline(time.Time) error { return nil }

// forwardedFor is the ID field (RFC 2971) Dovecot takes the reader's address
// from when alborz is in its login_trusted_networks.
const forwardedFor = "x-originating-ip"
