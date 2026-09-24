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

// dialIMAP connects to the domain's upstream IMAP server. It is the
// gatekeeper for the domain whitelist: unknown domains are rejected here.

// UTF-7 (RFC 2152) is in no charset index go-message consults.
func init() {
	for _, name := range []string{"utf-7", "unicode-1-1-utf-7"} {
		charset.RegisterEncoding(name, utf7.UTF7)
	}
}
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

// redacted stands where a credential was in the IMAP trace.
const redacted = "***REDACTED***"

// credential finds a command that carries a password: LOGIN, or an
// AUTHENTICATE with its initial response (RFC 4959).
var credential = regexp.MustCompile(`(?im)^(\S+) (LOGIN|AUTHENTICATE)\b`)

// serverSide is the server's side of the trace: untagged data, a
// continuation request, or a command's completion.
var serverSide = regexp.MustCompile(`^(?:[*+]|\S+ (?:OK|NO|BAD)\b)`)

// maskedTrace keeps credentials out of the IMAP trace -debug writes.
// The client's traffic and the server's reach it interleaved, a chunk
// at a time, so it works on chunks: a LOGIN or an AUTHENTICATE is cut
// after its name, and what the client sends until that command is
// answered - a literal password, a SASL response - is masked whole.
type maskedTrace struct {
	mu sync.Mutex
	w  io.Writer
	// tag is the command whose credentials may still follow.
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
