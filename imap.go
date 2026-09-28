package alborz

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net"
	"os"
	"regexp"
	"sync"

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
		debugWriter = &maskedTrace{w: os.Stderr}
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

const redacted = "***REDACTED***"

// credential matches LOGIN, or AUTHENTICATE with an initial response (RFC 4959).
var credential = regexp.MustCompile(`(?im)^(\S+) (LOGIN|AUTHENTICATE)\b`)

var serverSide = regexp.MustCompile(`^(?:[*+]|\S+ (?:OK|NO|BAD)\b)`)

// maskedTrace keeps credentials out of the -debug IMAP trace. It sees chunks,
// so it masks everything the client sends until the credential command completes.
type maskedTrace struct {
	mu  sync.Mutex
	w   io.Writer
	tag string
}

func (t *maskedTrace) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := p
	switch {
	case t.tag != "" && serverSide.Match(p):
		// The completion may follow other lines in the chunk.
		if bytes.HasPrefix(p, []byte(t.tag+" ")) || bytes.Contains(p, []byte("\n"+t.tag+" ")) {
			t.tag = ""
		}
	case t.tag != "":
		out = []byte(redacted + "\r\n")
	default:
		if m := credential.FindSubmatchIndex(p); m != nil {
			t.tag = string(p[m[2]:m[3]])
			out = append(bytes.Clone(p[:m[1]]), " "+redacted+"\r\n"...)
		}
	}
	if _, err := t.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
