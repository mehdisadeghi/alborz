package alborztest

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// SieveServer serves enough of ManageSieve (RFC 5804) for the filters pages: the commands the
// client sends, strings as literals.
type SieveServer struct {
	Addr    string
	mu      sync.Mutex
	scripts map[string]string
	active  string
}

func Sieve(t *testing.T) *SieveServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &SieveServer{Addr: ln.Addr().String(), scripts: map[string]string{}}
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

func (s *SieveServer) Upstream() string { return "sieve+insecure://" + s.Addr }

// Script is the script called name, or "".
func (s *SieveServer) Script(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scripts[name]
}

var sieveLiteral = regexp.MustCompile(`\{(\d+)\+?\}$`)

func (s *SieveServer) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	capability := `"IMPLEMENTATION" "fake"` + "\r\n" + `"SASL" "PLAIN"` + "\r\n" +
		`"SIEVE" "fileinto imap4flags copy include vacation"` + "\r\n" + `"VERSION" "1.0"` + "\r\nOK\r\n"
	io.WriteString(conn, capability)
	literal := func(v string) string { return fmt.Sprintf("{%d}\r\n%s", len(v), v) }
	for {
		var args []string
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		for {
			m := sieveLiteral.FindStringSubmatchIndex(line)
			if m == nil {
				break
			}
			args = append(args, strings.Fields(line[:m[0]])...)
			n, _ := strconv.Atoi(line[m[2]:m[3]])
			buf := make([]byte, n)
			if _, err := io.ReadFull(r, buf); err != nil {
				return
			}
			args = append(args, string(buf))
			rest, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(rest, "\r\n")
		}
		args = append(args, strings.Fields(line)...)
		s.mu.Lock()
		switch strings.ToUpper(args[0]) {
		case "AUTHENTICATE":
			io.WriteString(conn, "OK\r\n")
		case "CAPABILITY":
			io.WriteString(conn, capability)
		case "LISTSCRIPTS":
			for name := range s.scripts {
				active := ""
				if name == s.active {
					active = " ACTIVE"
				}
				fmt.Fprintf(conn, "%s%s\r\n", literal(name), active)
			}
			io.WriteString(conn, "OK\r\n")
		case "GETSCRIPT":
			if script, ok := s.scripts[args[1]]; ok {
				fmt.Fprintf(conn, "%s\r\nOK\r\n", literal(script))
			} else {
				io.WriteString(conn, "NO (NONEXISTENT) \"There is no script by that name\"\r\n")
			}
		case "PUTSCRIPT":
			s.scripts[args[1]] = args[2]
			io.WriteString(conn, "OK\r\n")
		case "SETACTIVE":
			s.active = args[1]
			io.WriteString(conn, "OK\r\n")
		case "DELETESCRIPT":
			delete(s.scripts, args[1])
			io.WriteString(conn, "OK\r\n")
		default:
			io.WriteString(conn, "NO\r\n")
		}
		s.mu.Unlock()
	}
}
