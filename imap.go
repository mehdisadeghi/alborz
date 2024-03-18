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

func (s *Server) dialIMAP() (*imapclient.Client, error) {
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
	if s.imap.tls {
		c, err = imapclient.DialTLS(s.imap.host, options)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to IMAPS server: %v", err)
		}
	} else if !s.imap.insecure {
		c, err = imapclient.DialStartTLS(s.imap.host, options)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to IMAP server: %v", err)
		}
	} else {
		conn, err := net.Dial("tcp", s.imap.host)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to IMAP server: %v", err)
		}
		c = imapclient.New(conn, options)
	}

	return c, err
}
