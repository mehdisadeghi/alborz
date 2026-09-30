package alborztest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"slices"
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
}

type imapConfig struct {
	caps           imap.CapSet
	failWholeFetch bool
	trickle        int
	cut            int64
	sorting        bool
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

// IMAP serves User, User2 and Elsewhere, with the rig's folders and messages.
func IMAP(t *testing.T, opts ...IMAPOption) *IMAPServer {
	t.Helper()
	// METADATA is not listed: the in-memory server does not implement it.
	c := imapConfig{caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}, imap.CapCondStore: {}, imap.CapSort: {}, imap.CapBinary: {}}}
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
			if c.failWholeFetch {
				return wholeFetchFails{mem.NewSession()}, nil, nil
			}
			return mem.NewSession(), nil, nil
		},
		Caps:         c.caps,
		InsecureAuth: true,
		DebugWriter:  counter{&s.wire},
	})
	go s.srv.Serve(ln)
	t.Cleanup(func() { s.srv.Close() })
	s.direct = ln.Addr().String()
	s.Addr = s.direct
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
				io.Copy(server, client)
				server.Close()
			}()
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
