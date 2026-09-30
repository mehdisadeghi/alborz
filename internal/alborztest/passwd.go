package alborztest

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
)

// PasswdServer changes the passwords of an IMAPServer's accounts over poppassd, answering as
// poppassd-ceti does; it refuses a new password shorter than four characters, as a policy would.
type PasswdServer struct {
	Addr string
	imap *IMAPServer
}

func Passwd(t *testing.T, imap *IMAPServer) *PasswdServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &PasswdServer{Addr: ln.Addr().String(), imap: imap}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *PasswdServer) Upstream() string { return "poppassd://" + s.Addr }

func WithPasswd(s *PasswdServer) Option { return Upstream(Domain, s.Upstream()) }

func (s *PasswdServer) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	read := func(command string) (string, bool) {
		line, err := r.ReadString('\n')
		keyword, arg, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		return arg, err == nil && strings.EqualFold(keyword, command)
	}
	say := func(reply string) { io.WriteString(conn, reply+"\r\n") }
	say("200 poppassd")
	user, ok := read("user")
	if !ok {
		return
	}
	say("200 Your password please")
	current, ok := read("pass")
	if !ok {
		return
	}
	if !s.imap.opens(user, current) {
		say("500 Old password is incorrect")
		return
	}
	say("200 Your new password please")
	next, ok := read("newpass")
	if !ok {
		return
	}
	if len(next) < 4 {
		say("500 Server pam_chauthtok error 20, password not changed")
		return
	}
	s.imap.SetPassword(user, next)
	say("200 Password changed")
	if _, ok := read("quit"); ok {
		say("200 Bye")
	}
}
