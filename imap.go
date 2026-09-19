package alborz

import (
	"fmt"
	"io"
	"mime"
	"net"
	"os"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
)

// dialIMAP connects to the domain's upstream IMAP server. It is the
// gatekeeper for the domain whitelist: unknown domains are rejected here.
func (s *Server) dialIMAP(domain, from string) (*imapclient.Client, error) {
	return s.dial(domain, from, nil)
}

// dialIMAPWatch dials with a handler for the updates a server sends
// unasked. It has to be given before the connection is made: the client
// copies its options, so one installed afterwards is never read.
//
// Only a watcher passes one. A connection serving requests must not,
// because the handler would run on whichever request happened to be
// reading the socket.
func (s *Server) dialIMAPWatch(domain string, unilateral *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	return s.dial(domain, "", unilateral)
}

func (s *Server) dial(domain, from string, unilateral *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	d, ok := s.upstreamsFor(domain)
	if !ok {
		return nil, UnknownDomainError{domain}
	}

	// TODO: don't print passwords to debug logs
	var debugWriter io.Writer
	if s.Options.Debug {
		debugWriter = os.Stderr
	}

	options := &imapclient.Options{
		DebugWriter: debugWriter,
		WordDecoder: &mime.WordDecoder{
			CharsetReader: charset.Reader,
		},
		Dialer:                &net.Dialer{Timeout: dialTimeout},
		UnilateralDataHandler: unilateral,
	}

	var c *imapclient.Client
	var err error
	if d.imap.tls {
		c, err = imapclient.DialTLS(d.imap.host, options)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to IMAPS server: %w", err)
		}
	} else if !d.imap.insecure {
		c, err = imapclient.DialStartTLS(d.imap.host, options)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
		}
	} else {
		conn, err := net.DialTimeout("tcp", d.imap.host, dialTimeout)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
		}
		c = imapclient.New(conn, options)
	}

	// Sent unanswered and without asking whether the server knows ID, so
	// neither the greeting nor an answer holds up the sign-in behind it.
	// A server that does not know the command answers BAD (RFC 3501 7.1),
	// and the sign-in goes ahead as it would have.
	if ip := net.ParseIP(from); ip != nil {
		c.ID(&imap.IDData{Raw: map[string]string{forwardedFor: ip.String()}})
	}
	return c, nil
}

// forwardedFor is the ID field (RFC 2971) Dovecot takes the reader's
// address from when alborz's own is in its login_trusted_networks: the
// server then logs, delays and has fail2ban ban the reader's address,
// not alborz's, which every reader shares. A server that does not trust
// alborz, or knows no such field, ignores it.
const forwardedFor = "x-originating-ip"
