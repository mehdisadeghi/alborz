package alborztest

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// IMAPServer is an in-memory mail server holding the rig's accounts, each seeded.
type IMAPServer struct {
	// Addr is where alborz reaches it, through the proxy an option put in front.
	Addr   string
	direct string
	srv    *imapserver.Server
	user   *imapmemserver.User
	wire   atomic.Int64
	armed  chan struct{}
	sorts  atomic.Int32
	thread atomic.Int32
	// passwords holds the passwords changed from Password.
	passwords sync.Map
}

type imapConfig struct {
	caps           imap.CapSet
	failWholeFetch bool
	trickle        int
	cut            int64
	sorting        bool
	metadata       bool
}

type IMAPOption func(*imapConfig)

// Caps replaces what the server offers.
func Caps(caps ...imap.Cap) IMAPOption {
	return func(c *imapConfig) {
		c.caps = imap.CapSet{}
		for _, cap := range caps {
			c.caps[cap] = struct{}{}
		}
	}
}

// Without takes caps from what the server offers.
func Without(caps ...imap.Cap) IMAPOption {
	return func(c *imapConfig) {
		for _, cap := range caps {
			delete(c.caps, cap)
		}
	}
}

// FailWholeFetch refuses to hand over a whole message, as a server might when it went meanwhile.
func FailWholeFetch() IMAPOption { return func(c *imapConfig) { c.failWholeFetch = true } }

// Trickle passes the server's bytes in pieces of at most chunk bytes, each arriving on its own.
func Trickle(chunk int) IMAPOption { return func(c *imapConfig) { c.trickle = chunk } }

// Cut drops, once Arm is called, the connection on which the server has sent limit more bytes.
func Cut(limit int64) IMAPOption { return func(c *imapConfig) { c.cut = limit } }

// Sorting offers SORT and THREAD, which the in-memory server lacks: it asks SEARCH instead and
// answers in its order, reversed where the sort was, or two messages to a conversation.
func Sorting() IMAPOption { return func(c *imapConfig) { c.sorting = true } }

// Metadata offers METADATA-SERVER (RFC 5464), which the in-memory server lacks: a proxy in front
// keeps each account's server entries under /private and answers GETMETADATA and SETMETADATA.
func Metadata() IMAPOption { return func(c *imapConfig) { c.metadata = true } }

// IMAP serves User, User2 and Elsewhere, with the rig's folders and messages.
func IMAP(t *testing.T, opts ...IMAPOption) *IMAPServer {
	t.Helper()
	// METADATA, SORT and THREAD are not listed: the in-memory server does not implement them; Metadata
	// offers the first, Sorting the last two.
	c := imapConfig{caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}, imap.CapCondStore: {}, imap.CapBinary: {}}}
	for _, o := range opts {
		o(&c)
	}
	mem := imapmemserver.New()
	s := &IMAPServer{armed: make(chan struct{}), user: seedFirst(t)}
	mem.AddUser(s.user)
	mem.AddUser(seedSecond(t))
	mem.AddUser(seedElsewhere(t))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.srv = imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			sess := changedPasswords{mem.NewSession().(imapserver.SessionIMAP4rev2), s}
			if c.failWholeFetch {
				return wholeFetchFails{sess}, nil, nil
			}
			return sess, nil, nil
		},
		Caps:         c.caps,
		InsecureAuth: true,
		DebugWriter:  counter{&s.wire},
	})
	go s.srv.Serve(ln)
	t.Cleanup(func() { s.srv.Close() })
	s.direct = ln.Addr().String()
	s.Addr = s.direct
	// First, so it reads the server's own capabilities.
	if c.metadata {
		s.Addr = s.metadataProxy(t, s.Addr)
	}
	if c.trickle > 0 {
		s.Addr = proxy(t, s.Addr, trickle(c.trickle))
	}
	if c.cut > 0 {
		s.Addr = proxy(t, s.Addr, s.cut(c.cut))
	}
	if c.sorting {
		s.Addr = s.sortingProxy(t, s.Addr)
	}
	return s
}

// Upstream is the server as alborz is told of it.
func (s *IMAPServer) Upstream() string { return "imap+insecure://" + s.Addr }

// Close stops the server, as an outage does.
func (s *IMAPServer) Close() { s.srv.Close() }

// Arm starts the countdown of Cut.
func (s *IMAPServer) Arm() { close(s.armed) }

// Wire is how many bytes the server has exchanged.
func (s *IMAPServer) Wire() int64 { return s.wire.Load() }

// Sorts and Threads count the SORT and THREAD commands the Sorting proxy answered.
func (s *IMAPServer) Sorts() int32   { return s.sorts.Load() }
func (s *IMAPServer) Threads() int32 { return s.thread.Load() }

// Append puts raw into User's mbox.
func (s *IMAPServer) Append(t *testing.T, mbox, raw string, flags ...imap.Flag) imap.UID {
	t.Helper()
	data, err := s.user.Append(mbox, literal{strings.NewReader(raw), int64(len(raw))}, &imap.AppendOptions{Flags: flags})
	if err != nil {
		t.Fatal(err)
	}
	return data.UID
}

// Dial is a client signed in as User, straight to the server.
func (s *IMAPServer) Dial(t *testing.T) *imapclient.Client {
	t.Helper()
	c, err := imapclient.DialInsecure(s.direct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Login(User, Password).Wait(); err != nil {
		t.Fatal(err)
	}
	return c
}

type literal struct {
	io.Reader
	n int64
}

func (l literal) Size() int64 { return l.n }

func appendTo(t *testing.T, u *imapmemserver.User, mbox, raw string, flags ...imap.Flag) {
	t.Helper()
	if _, err := u.Append(mbox, literal{strings.NewReader(raw), int64(len(raw))}, &imap.AppendOptions{Flags: flags}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

type folder struct {
	name string
	attr imap.MailboxAttr
}

func newUser(t *testing.T, name string, folders []folder) *imapmemserver.User {
	t.Helper()
	u := imapmemserver.NewUser(name, Password)
	for _, f := range folders {
		var opts *imap.CreateOptions
		if f.attr != "" {
			opts = &imap.CreateOptions{SpecialUse: []imap.MailboxAttr{f.attr}}
		}
		if err := u.Create(f.name, opts); err != nil {
			t.Fatalf("create %s: %v", f.name, err)
		}
	}
	return u
}

var rigFolders = []folder{
	{"INBOX", ""}, {"Drafts", imap.MailboxAttrDrafts}, {"Sent", imap.MailboxAttrSent},
	{"Junk", imap.MailboxAttrJunk}, {"Trash", imap.MailboxAttrTrash}, {"Archive", imap.MailboxAttrArchive},
	// A nested folder's name carries the separator, which every URL must escape.
	{"INBOX/Lists", ""},
}

// seedFirst gives User five messages in INBOX and INBOX/Lists, each rendering different toolbars and rows.
func seedFirst(t *testing.T) *imapmemserver.User {
	t.Helper()
	u := newUser(t, User, rigFolders)
	for _, m := range []struct{ from, subj, extra, body string }{
		{"Eve <eve@example.org>", "Ideas for the redesign",
			"Content-Type: text/plain; charset=UTF-8\r\n", "Body.\r\n"},
		{"Frank <frank@example.org>", "[discuss] Threading",
			"List-Id: Rig discuss <discuss.lists.example.org>\r\n" +
				"List-Post: <mailto:discuss@lists.example.org>\r\n" +
				"List-Unsubscribe: <https://lists.example.org/u>\r\n" +
				"Content-Type: text/plain; charset=UTF-8\r\n", "Body.\r\n"},
		{"Shop <news@shop.example>", "Our terms changed",
			"List-Id: campaign <c.list-id.shop.example>\r\n" +
				"List-Unsubscribe: <https://shop.example/u>, <mailto:leave@shop.example>\r\n" +
				"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n" +
				"Content-Type: text/plain; charset=UTF-8\r\n", "Body.\r\n"},
		// The sanitizer builds the part's raw URL, which must escape the folder's separator.
		{"Hal <hal@example.org>", "Inline picture",
			"Content-Type: multipart/related; boundary=r1\r\n",
			"--r1\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n" +
				"<p>See <img src=\"cid:pic@rig\"></p>\r\n" +
				"--r1\r\nContent-Type: image/png\r\nContent-ID: <pic@rig>\r\n\r\nPNG\r\n" +
				"--r1--\r\n"},
		{"Bob <bob@example.org>", "Attached docs",
			"Content-Type: multipart/mixed; boundary=b1\r\n",
			"--b1\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nBody.\r\n" +
				"--b1\r\nContent-Type: application/pdf\r\n" +
				"Content-Disposition: attachment; filename=\"doc.pdf\"\r\n\r\n%PDF-\r\n" +
				// An unnamed attachment exercises the branch named ones skip.
				"--b1\r\nContent-Type: application/octet-stream\r\n" +
				"Content-Disposition: attachment\r\n\r\nraw\r\n" +
				"--b1--\r\n"},
	} {
		raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n"+
			"Message-ID: <%d@test>\r\nMIME-Version: 1.0\r\n%s\r\n%s",
			m.from, User, m.subj, time.Now().Format(time.RFC1123Z), time.Now().UnixNano(), m.extra, m.body)
		for _, mbox := range []string{"INBOX", "INBOX/Lists"} {
			appendTo(t, u, mbox, raw)
		}
	}
	return u
}

// seedSecond gives User2 a draft, which one deleted on the wrong account would take, and a message.
func seedSecond(t *testing.T) *imapmemserver.User {
	t.Helper()
	u := newUser(t, User2, rigFolders)
	appendTo(t, u, "Drafts", fmt.Sprintf("From: %s\r\nTo: friend@example.org\r\nSubject: unsent\r\n"+
		"Message-ID: <draft@test>\r\nContent-Type: text/plain\r\n\r\nLater.\r\n", User2), imap.FlagDraft)
	appendTo(t, u, "INBOX", fmt.Sprintf("From: pat@example.org\r\nTo: %s\r\nSubject: for the second account\r\n"+
		"Message-ID: <second-inbox@test>\r\nContent-Type: text/plain\r\n\r\nHello.\r\n", User2))
	return u
}

// seedElsewhere gives Elsewhere an INBOX and nothing else.
func seedElsewhere(t *testing.T) *imapmemserver.User {
	t.Helper()
	u := newUser(t, Elsewhere, []folder{{"INBOX", ""}})
	appendTo(t, u, "INBOX", fmt.Sprintf("From: gil@example.org\r\nTo: %s\r\nSubject: from elsewhere\r\n"+
		"Message-ID: <elsewhere@test>\r\nContent-Type: text/plain\r\n\r\nHello.\r\n", Elsewhere))
	return u
}

// SetPassword changes user's password, as the mail host's password server does.
func (s *IMAPServer) SetPassword(user, password string) { s.passwords.Store(user, password) }

func (s *IMAPServer) opens(user, password string) bool {
	want, changed := s.passwords.Load(user)
	if !changed {
		want = Password
	}
	return password == want
}

// changedPasswords signs in with the passwords SetPassword gave, which the in-memory server cannot change.
type changedPasswords struct {
	imapserver.SessionIMAP4rev2
	s *IMAPServer
}

func (c changedPasswords) Login(username, password string) error {
	if !c.s.opens(username, password) {
		return imapserver.ErrAuthFailed
	}
	return c.SessionIMAP4rev2.Login(username, Password)
}

type wholeFetchFails struct{ imapserver.Session }

func (s wholeFetchFails) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	for _, section := range options.BodySection {
		if section.Specifier == imap.PartSpecifierNone && len(section.Part) == 0 {
			return errors.New("the message is gone")
		}
	}
	return s.Session.Fetch(w, numSet, options)
}

type counter struct{ n *atomic.Int64 }

func (c counter) Write(b []byte) (int, error) {
	c.n.Add(int64(len(b)))
	return len(b), nil
}

// proxy passes IMAP traffic through, the server's side by relay.
func proxy(t *testing.T, upstream string, relay func(client, server net.Conn)) string {
	t.Helper()
	return proxyPair(t, upstream, func(client, server net.Conn) {
		go func() {
			io.Copy(server, client)
			server.Close()
		}()
		relay(client, server)
	})
}

// proxyPair hands relay both sides of each connection, closed once it returns.
func proxyPair(t *testing.T, upstream string, relay func(client, server net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", upstream)
			if err != nil {
				client.Close()
				continue
			}
			go func() {
				defer client.Close()
				defer server.Close()
				relay(client, server)
			}()
		}
	}()
	return ln.Addr().String()
}

func trickle(chunk int) func(client, server net.Conn) {
	return func(client, server net.Conn) {
		buf := make([]byte, chunk)
		for {
			n, err := server.Read(buf)
			if _, werr := client.Write(buf[:n]); werr != nil || err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func (s *IMAPServer) cut(limit int64) func(client, server net.Conn) {
	return func(client, server net.Conn) {
		buf := make([]byte, 32<<10)
		var sent int64
		for {
			n, err := server.Read(buf)
			select {
			case <-s.armed:
				if sent += int64(n); sent > limit {
					return
				}
			default:
			}
			if _, werr := client.Write(buf[:n]); werr != nil || err != nil {
				return
			}
		}
	}
}

var (
	sortCommand   = regexp.MustCompile(`^(\S+ (?:UID )?)SORT \(([^)]*)\) \S+ (.*)$`)
	threadCommand = regexp.MustCompile(`^(\S+ (?:UID )?)THREAD \S+ \S+ (.*)$`)
)

func (s *IMAPServer) sortingProxy(t *testing.T, upstream string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", upstream)
			if err != nil {
				client.Close()
				continue
			}
			// The tags the server has yet to answer, in order; the one in front owns an untagged SEARCH.
			var mu sync.Mutex
			type pending struct {
				tag                        string
				sorted, reversed, threaded bool
			}
			var queue []pending
			go func() {
				defer server.Close()
				r := bufio.NewReader(client)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					body := strings.TrimRight(line, "\r\n")
					if m := threadCommand.FindStringSubmatch(body); m != nil {
						s.thread.Add(1)
						mu.Lock()
						tag, _, _ := strings.Cut(m[1], " ")
						queue = append(queue, pending{tag: tag, threaded: true})
						mu.Unlock()
						line = m[1] + "SEARCH " + m[2] + "\r\n"
					} else if m := sortCommand.FindStringSubmatch(body); m != nil {
						s.sorts.Add(1)
						mu.Lock()
						tag, _, _ := strings.Cut(m[1], " ")
						queue = append(queue, pending{tag: tag, sorted: true, reversed: strings.HasPrefix(m[2], "REVERSE ")})
						mu.Unlock()
						line = m[1] + "SEARCH " + m[3] + "\r\n"
					} else if tag, _, ok := strings.Cut(body, " "); ok && tag != "*" {
						mu.Lock()
						queue = append(queue, pending{tag: tag})
						mu.Unlock()
					}
					if _, err := io.WriteString(server, line); err != nil {
						return
					}
				}
			}()
			go func() {
				defer client.Close()
				r := bufio.NewReader(server)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					body := strings.TrimRight(line, "\r\n")
					line = strings.Replace(line, "CAPABILITY ", "CAPABILITY SORT THREAD=REFERENCES ", 1)
					mu.Lock()
					if nums, ok := strings.CutPrefix(body, "* SEARCH"); ok && len(queue) > 0 && queue[0].threaded {
						line = "* THREAD "
						for pair := range slices.Chunk(strings.Fields(nums), 2) {
							line += "(" + strings.Join(pair, " ") + ")"
						}
						line = strings.TrimRight(line, " ") + "\r\n"
					}
					if nums, ok := strings.CutPrefix(body, "* SEARCH"); ok && len(queue) > 0 && queue[0].sorted {
						uids := strings.Fields(nums)
						if queue[0].reversed {
							slices.Reverse(uids)
						}
						line = strings.TrimRight("* SORT "+strings.Join(uids, " "), " ") + "\r\n"
					}
					if tag, _, ok := strings.Cut(body, " "); ok {
						if i := slices.IndexFunc(queue, func(p pending) bool { return p.tag == tag }); i >= 0 {
							queue = slices.Delete(queue, i, i+1)
						}
					}
					mu.Unlock()
					if _, err := io.WriteString(client, line); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// metadata are the entries of each account, by name.
type metadata struct {
	mu      sync.Mutex
	entries map[string]map[string][]byte
}

// A line ending in a literal's announcement, {n} or the non-synchronizing {n+}.
var literalAhead = regexp.MustCompile(`\{(\d+)(\+?)\}\r\n$`)

func literalSize(line string) (n int, sync, ok bool) {
	m := literalAhead.FindStringSubmatch(line)
	if m == nil {
		return 0, false, false
	}
	n, _ = strconv.Atoi(m[1])
	return n, m[2] == "", true
}

func (s *IMAPServer) metadataProxy(t *testing.T, upstream string) string {
	t.Helper()
	md := &metadata{entries: map[string]map[string][]byte{}}
	return proxyPair(t, upstream, func(client, server net.Conn) {
		var wmu sync.Mutex
		send := func(b []byte) error {
			wmu.Lock()
			defer wmu.Unlock()
			_, err := client.Write(b)
			return err
		}
		go func() {
			// Half closed, so the server's last answers still reach the client.
			defer server.(*net.TCPConn).CloseWrite()
			r := bufio.NewReader(client)
			var user string
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				tag, rest, _ := strings.Cut(line, " ")
				command, _, _ := strings.Cut(strings.TrimRight(rest, "\r\n"), " ")
				switch strings.ToUpper(command) {
				case "GETMETADATA", "SETMETADATA":
					cmd := []byte(line)
					for n, sync, ok := literalSize(line); ok; n, sync, ok = literalSize(line) {
						if sync && send([]byte("+ Ready\r\n")) != nil {
							return
						}
						lit := make([]byte, n)
						if _, err := io.ReadFull(r, lit); err != nil {
							return
						}
						if line, err = r.ReadString('\n'); err != nil {
							return
						}
						cmd = append(append(cmd, lit...), line...)
					}
					if send(md.answer(user, tag, cmd)) != nil {
						return
					}
					continue
				case "LOGIN":
					if args := imapArgs(line); len(args) > 2 {
						user = args[2].s
					}
				}
				if _, err := io.WriteString(server, line); err != nil {
					return
				}
				for n, _, ok := literalSize(line); ok; n, _, ok = literalSize(line) {
					if _, err := io.CopyN(server, r, int64(n)); err != nil {
						return
					}
					if line, err = r.ReadString('\n'); err != nil {
						return
					}
					if _, err := io.WriteString(server, line); err != nil {
						return
					}
				}
			}
		}()
		r := bufio.NewReader(server)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			out := []byte(strings.Replace(line, "CAPABILITY IMAP4", "CAPABILITY METADATA-SERVER IMAP4", 1))
			for n, _, ok := literalSize(line); ok; n, _, ok = literalSize(line) {
				lit := make([]byte, n)
				if _, err := io.ReadFull(r, lit); err != nil {
					return
				}
				if line, err = r.ReadString('\n'); err != nil {
					return
				}
				out = append(append(out, lit...), line...)
			}
			if send(out) != nil {
				return
			}
		}
	})
}

// answer serves one GETMETADATA or SETMETADATA of user's on the server, the mailbox "".
func (md *metadata) answer(user, tag string, cmd []byte) []byte {
	all := imapArgs(string(cmd))
	command, args := strings.ToUpper(all[1].s), all[2:]
	depth := "0"
	if command == "GETMETADATA" && len(args) > 0 && args[0].open {
		i := slices.IndexFunc(args, func(a imapArg) bool { return a.close })
		for j := 1; j+1 < i; j += 2 {
			if strings.EqualFold(args[j].s, "DEPTH") {
				depth = strings.ToLower(args[j+1].s)
			}
		}
		args = args[i+1:]
	}
	if len(args) < 2 || args[0].s != "" {
		return fmt.Appendf(nil, "%s NO [NONEXISTENT] only the server's entries are kept\r\n", tag)
	}
	var names []imapArg
	for _, a := range args[1:] {
		if !a.open && !a.close {
			names = append(names, a)
		}
	}
	md.mu.Lock()
	defer md.mu.Unlock()
	mine := md.entries[user]
	if mine == nil {
		mine = map[string][]byte{}
		md.entries[user] = mine
	}
	if command == "SETMETADATA" {
		for i := 0; i+1 < len(names); i += 2 {
			if !strings.HasPrefix(names[i].s, "/private/") {
				return fmt.Appendf(nil, "%s BAD only /private entries are kept\r\n", tag)
			}
			if names[i+1].nil {
				delete(mine, names[i].s)
			} else {
				mine[names[i].s] = []byte(names[i+1].s)
			}
		}
		return fmt.Appendf(nil, "%s OK SETMETADATA done\r\n", tag)
	}
	var values [][]byte
	value := func(name string, v []byte, found bool) {
		if !found {
			values = append(values, fmt.Appendf(nil, "%q NIL", name))
			return
		}
		values = append(values, fmt.Appendf(nil, "%q {%d}\r\n%s", name, len(v), v))
	}
	for _, n := range names {
		v, found := mine[n.s]
		value(n.s, v, found)
		if depth == "0" {
			continue
		}
		var below []string
		for name := range mine {
			rest, ok := strings.CutPrefix(name, n.s+"/")
			if ok && (depth == "infinity" || !strings.Contains(rest, "/")) {
				below = append(below, name)
			}
		}
		slices.Sort(below)
		for _, name := range below {
			value(name, mine[name], true)
		}
	}
	return fmt.Appendf(nil, "* METADATA \"\" (%s)\r\n%s OK GETMETADATA done\r\n", bytes.Join(values, []byte(" ")), tag)
}

// imapArg is one argument of a command: a string, an atom, NIL, or a parenthesis.
type imapArg struct {
	s                string
	nil, open, close bool
}

// imapArgs splits a command, literals included, into its tag, its name and its arguments.
func imapArgs(cmd string) []imapArg {
	var out []imapArg
	for i := 0; i < len(cmd); {
		switch c := cmd[i]; {
		case c == ' ' || c == '\r' || c == '\n':
			i++
		case c == '(':
			out = append(out, imapArg{open: true})
			i++
		case c == ')':
			out = append(out, imapArg{close: true})
			i++
		case c == '"':
			var b strings.Builder
			for i++; i < len(cmd) && cmd[i] != '"'; i++ {
				if cmd[i] == '\\' {
					i++
				}
				b.WriteByte(cmd[i])
			}
			out = append(out, imapArg{s: b.String()})
			i++
		case c == '{':
			end := strings.Index(cmd[i:], "}\r\n")
			n, _ := strconv.Atoi(strings.TrimSuffix(cmd[i+1:i+end], "+"))
			start := i + end + 3
			out = append(out, imapArg{s: cmd[start : start+n]})
			i = start + n
		default:
			j := i
			for j < len(cmd) && !strings.ContainsRune(" ()\r\n", rune(cmd[j])) {
				j++
			}
			out = append(out, imapArg{s: cmd[i:j], nil: strings.EqualFold(cmd[i:j], "NIL")})
			i = j
		}
	}
	return out
}
