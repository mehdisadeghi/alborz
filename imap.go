package alps

import (
	"fmt"
	"io"
	"mime"
	"net"
	"os"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
)

// dialIMAP connects to the domain's upstream IMAP server. It is the
// gatekeeper for the domain whitelist: unknown domains are rejected here.
func (s *Server) dialIMAP(domain string) (*imapclient.Client, error) {
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
		conn, err := net.Dial("tcp", d.imap.host)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
		}
		c = imapclient.New(conn, options)
	}

	return c, err
}
