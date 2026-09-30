package alborz

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/textproto"
	"strings"
	"time"
)

// poppassdPort is poppassd's registered port.
const poppassdPort = "106"

// passwdServer is a domain's poppassd service; network is "" when there is none.
type passwdServer struct {
	network, addr string
	// local sends the address's local part as the user, as system accounts are named.
	local bool
}

// PasswordRefusal is the password server's no, in its own words.
type PasswordRefusal struct{ Text string }

func (r PasswordRefusal) Error() string { return "password server refused: " + r.Text }

func (s *Server) parsePasswdUpstream(domain string) error {
	u, err := s.Upstream(domain, "poppassd", "poppassd+unix")
	if _, ok := err.(*NoUpstreamError); ok {
		return nil
	} else if err != nil {
		return fmt.Errorf("domain %q: %v", domain, err)
	}
	// There is no SRV record for poppassd.
	if u.Scheme == "" {
		return nil
	}
	var p passwdServer
	switch u.Scheme {
	case "poppassd":
		if u.Hostname() == "" {
			return fmt.Errorf("domain %q: %v names no host", domain, u)
		}
		// poppassd carries passwords in plain text.
		if ip, err := netip.ParseAddr(u.Hostname()); u.Hostname() != "localhost" && (err != nil || !ip.IsLoopback()) {
			return fmt.Errorf("domain %q: %v: poppassd is plain text; give a loopback address or a Unix socket", domain, u)
		}
		p.network, p.addr = "tcp", u.Host
		if u.Port() == "" {
			p.addr = net.JoinHostPort(u.Hostname(), poppassdPort)
		}
	case "poppassd+unix":
		if u.Path == "" {
			return fmt.Errorf("domain %q: %v names no socket", domain, u)
		}
		p.network, p.addr = "unix", u.Path
	}
	for key, values := range u.Query() {
		if key != "user" || len(values) != 1 || values[0] != "local" {
			return fmt.Errorf("domain %q: %v: the one option is user=local", domain, u)
		}
		p.local = true
	}
	s.domains[domain].passwd = p
	if s.Options.Debug {
		s.log.Printf("Domain %q: configured password server: %v", domain, u)
	}
	return nil
}

// PasswdEnabled reports whether the domain has a password server.
func (s *Server) PasswdEnabled(domain string) bool {
	d, ok := s.upstreamsFor(domain)
	return ok && d.passwd.network != ""
}

// ChangePassword asks the domain's password server to change the login's password. A refused
// current password counts against the reader as a refused sign-in does (ADR 30). The password
// the login keeps for DAV and the account's own key are sealed under the new one.
func (s *Server) ChangePassword(ctx context.Context, l *Login, current, next, from string) error {
	d, _ := s.upstreamsFor(l.domain)
	if d.passwd.network == "" {
		return errors.ErrUnsupported
	}
	user := l.username
	if d.passwd.local {
		user, _, _ = strings.Cut(user, "@")
	}
	err := s.signInRefused.attempt(readerKey(from, l.username), func() error {
		return d.passwd.change(user, current, next)
	})
	if err != nil {
		return err
	}
	// The password has changed; a DAV password left sealed is asked for again (ADR 5).
	if err := l.rewrap(ctx, next); err != nil {
		s.log.Printf("%s: the DAV password stays sealed under the old mail password: %v", l.username, err)
	}
	if err := s.rewrapKey(ctx, l, next); err != nil {
		s.log.Printf("%s: the account's key stays wrapped under the old mail password: %v", l.username, err)
	}
	return nil
}

// change speaks poppassd: a greeting, then USER, PASS, NEWPASS and QUIT, each answered 200 or
// refused 500 with a reason. poppassd-ceti waits three seconds before refusing a password.
func (p passwdServer) change(user, current, next string) error {
	for _, v := range []string{user, current, next} {
		if strings.ContainsAny(v, "\r\n\x00") {
			return errors.New("poppassd: a line break or NUL cannot be sent")
		}
	}
	conn, err := net.DialTimeout(p.network, p.addr, dialTimeout)
	if err != nil {
		return UpstreamError{Service: "password", cause: err}
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(signInTimeout))
	c := textproto.NewConn(conn)
	ask := func(format string, args ...any) error {
		if format != "" {
			if _, err := c.Cmd(format, args...); err != nil {
				return err
			}
		}
		_, _, err := c.ReadCodeLine(2)
		var no *textproto.Error
		if errors.As(err, &no) {
			return PasswordRefusal{Text: no.Msg}
		}
		var slow net.Error
		if errors.As(err, &slow) && slow.Timeout() {
			return UpstreamError{Service: "password", After: signInTimeout, cause: err}
		}
		if err != nil {
			return UpstreamError{Service: "password", cause: err}
		}
		return nil
	}
	// A refusal before NEWPASS is of the account or its password.
	refused := func(err error) error {
		if errors.As(err, new(PasswordRefusal)) {
			return AuthError{err}
		}
		return err
	}
	if err := ask(""); err != nil {
		return err
	}
	if err := ask("user %s", user); err != nil {
		return refused(err)
	}
	if err := ask("pass %s", current); err != nil {
		return refused(err)
	}
	if err := ask("newpass %s", next); err != nil {
		return err
	}
	// The password has changed; how the server takes leave does not matter.
	ask("quit")
	return nil
}
