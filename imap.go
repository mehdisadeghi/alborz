package alborz

import (
	"fmt"
	"io"
	"mime"
	"net"
	"os"

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
	return s.dial(domain, from, nil)
}

// dialIMAPWatch dials with a handler for unsolicited updates; only a watcher
// passes one, or it would run on whichever request reads the socket.
func (s *Server) dialIMAPWatch(domain string, unilateral *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	return s.dial(domain, "", unilateral)
}

func (s *Server) dial(domain, from string, unilateral *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	d, ok := s.upstreamsFor(domain)
	if !ok {
		return nil, UnknownDomainError{domain}
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

	// Not awaited, so ID never holds up the sign-in; a server without ID answers BAD.
	if ip := net.ParseIP(from); ip != nil {
		c.ID(&imap.IDData{Raw: map[string]string{forwardedFor: ip.String()}})
	}
	return c, nil
}

// forwardedFor is the ID field (RFC 2971) Dovecot takes the reader's address
// from when alborz is in its login_trusted_networks.
const forwardedFor = "x-originating-ip"
