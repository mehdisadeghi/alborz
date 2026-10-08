package filters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/quotedprintable"
	"regexp"
	"strconv"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/lang"
)

// Days between two automatic answers to one sender (RFC 5230 4.1).
const (
	ReplyDays    = 7
	MaxReplyDays = 30
	repliesKey   = "sieve.replies"
	MaxReplies   = 20
)

// ErrTooManyReplies refuses a reply past MaxReplies.
var ErrTooManyReplies = errors.New("too many replies")

// Reply is one automatic answer; Addresses limit it (RFC 5230 4.5), From and Until bound its days (RFC 5260).
type Reply struct {
	Name      string
	Subject   string
	Text      string
	Days      int
	Addresses []string
	From      string
	Until     string
}

// Replies is what the store holds for the page.
type Replies struct {
	Items []Reply
}

func LoadReplies(ctx context.Context, s alborz.Store) (*Replies, error) {
	r := &Replies{}
	if err := s.Get(ctx, repliesKey, r); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	return r, nil
}

// UpdateReplies has edit change the account's replies as stored, taking turns with every other change.
func UpdateReplies(ctx context.Context, l *alborz.Login, edit func(*Replies) error) error {
	r := &Replies{}
	return l.Update(ctx, repliesKey, r, func() error { return edit(r) })
}

// Put replaces the reply at index, or adds one when index holds none; made is where it was added, else -1.
func (r *Replies) Put(index int, v Reply) (made int, err error) {
	switch {
	case index >= 0 && index < len(r.Items):
		r.Items[index] = v
		return -1, nil
	case len(r.Items) >= MaxReplies:
		return -1, ErrTooManyReplies
	}
	r.Items = append(r.Items, v)
	return len(r.Items) - 1, nil
}

// ActiveReply is the index in items of the reply the script sends, -1 when none;
// byHand says the script is in a shape the page did not write.
func ActiveReply(c alborz.SieveClient, items []Reply) (active int, loaded string, byHand bool, err error) {
	return loadScript(c, VacationScript, -1, func(content string) (int, bool) {
		_, written := readReply(content)
		return activeIndex(items, content), written
	})
}

// ReplaceReply sends v at once if the reply at index in was is the one being sent.
func ReplaceReply(c alborz.SieveClient, loaded string, was []Reply, index int, v Reply) (sent bool, err error) {
	err = rewrite(c, VacationScript, loaded, func(current string, _ bool) (string, error) {
		if activeIndex(was, current) != index {
			return current, nil
		}
		sent = true
		return v.script(), nil
	})
	return sent, err
}

// WithdrawReply stops gone if it is the reply being sent.
func WithdrawReply(c alborz.SieveClient, loaded string, gone Reply) error {
	return rewrite(c, VacationScript, loaded, func(current string, _ bool) (string, error) {
		if sent, ok := readReply(current); ok && sent.script() == gone.script() {
			return "", nil
		}
		return current, nil
	})
}

// ActivateReply sends v; ErrByHand leaves a script the page did not write alone.
func ActivateReply(c alborz.SieveClient, loaded string, v Reply) error {
	return send(c, loaded, v.script())
}

// DeactivateReply stops the reply being sent, which deletes the script.
func DeactivateReply(c alborz.SieveClient, loaded string) error {
	return send(c, loaded, "")
}

func send(c alborz.SieveClient, loaded, script string) error {
	return rewrite(c, VacationScript, loaded, func(current string, exists bool) (string, error) {
		if exists {
			if _, ok := readReply(current); !ok {
				return "", ErrByHand
			}
		}
		return script, nil
	})
}

func (v Reply) dated() bool { return v.From != "" || v.Until != "" }

func (v Reply) script() string {
	var b strings.Builder
	b.WriteString("# Auto-reply, composed on the Auto-reply page.\n")
	fmt.Fprintf(&b, "# reply: %s\n", strings.ReplaceAll(v.Name, "\n", " "))
	need := []string{"vacation"}
	if v.dated() {
		need = append(need, "date", "relational")
	}
	b.WriteString(requireLine(need))
	indent := ""
	if v.dated() {
		var tests []string
		if v.From != "" {
			tests = append(tests, fmt.Sprintf(`currentdate :value "ge" "date" %s`, quote(v.From)))
		}
		if v.Until != "" {
			tests = append(tests, fmt.Sprintf(`currentdate :value "le" "date" %s`, quote(v.Until)))
		}
		fmt.Fprintf(&b, "if allof(%s) {\n", strings.Join(tests, ", "))
		indent = "    "
	}
	fmt.Fprintf(&b, "%svacation :days %d", indent, v.Days)
	if v.Subject != "" {
		fmt.Fprintf(&b, " :subject %s", quote(v.Subject))
	}
	if len(v.Addresses) > 0 {
		q := make([]string, len(v.Addresses))
		for i, a := range v.Addresses {
			q[i] = quote(a)
		}
		fmt.Fprintf(&b, " :addresses [%s]", strings.Join(q, ", "))
	}
	fmt.Fprintf(&b, " :mime text:\n%s.\n;\n", dotStuff(replyEntity(v.Text)))
	if v.dated() {
		b.WriteString("}\n")
	}
	return b.String()
}

// Quoted-printable never writes "=_", so no encoded line can equal the delimiter (RFC 2046 5.1.1).
const replyBoundary = "=_alborz-reply"

// replyEntity carries an HTML alternative for the text's direction; quoted-printable keeps the raw script readable.
func replyEntity(text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\n\n", replyBoundary)
	for _, part := range []struct{ kind, body string }{{"text/plain", text + "\n"}, {"text/html", lang.HTMLAlternative(text)}} {
		fmt.Fprintf(&b, "--%s\nContent-Type: %s; charset=utf-8\nContent-Transfer-Encoding: quoted-printable\n\n", replyBoundary, part.kind)
		w := quotedprintable.NewWriter(&b)
		_, err := io.WriteString(w, part.body)
		if err == nil {
			err = w.Close()
		}
		if err != nil {
			panic(err) // a strings.Builder takes every write
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "--%s--\n", replyBoundary)
	return strings.ReplaceAll(b.String(), "\r\n", "\n")
}

// dotStuff and dotUnstuff apply the multi-line string rule of RFC 5228 2.4.2.
func dotStuff(s string) string { return strings.ReplaceAll("\n"+s, "\n.", "\n..")[1:] }

func dotUnstuff(s string) string { return strings.ReplaceAll("\n"+s, "\n..", "\n.")[1:] }

func replyText(entity string) (string, bool) {
	parts := strings.Split(entity, "--"+replyBoundary+"\n")
	if len(parts) < 2 {
		return "", false
	}
	_, body, ok := strings.Cut(parts[1], "\n\n")
	if !ok {
		return "", false
	}
	decoded, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
	if err != nil {
		return "", false
	}
	return strings.TrimRight(string(decoded), "\n"), true
}

var (
	replyName    = regexp.MustCompile(`(?m)^# reply: (.*)$`)
	replyGuard   = regexp.MustCompile(`(?m)^if allof\((.*)\) \{$`)
	replyDate    = regexp.MustCompile(`currentdate :value "(ge|le)" "date" ` + quoted)
	vacationLine = regexp.MustCompile(`(?s)^\s*vacation :days (\d+)(?: :subject ` + quoted + `)?(?: :addresses \[(.*?)\])? :mime text:\n(.*)\n\.\n;$`)
)

// readReply returns ok false for a script the page did not write.
func readReply(content string) (v Reply, ok bool) {
	if m := replyName.FindStringSubmatch(content); m != nil {
		v.Name = m[1]
	}
	// The command runs to the script's end, entity included; only what precedes it is read by line.
	at := strings.Index(content, "vacation :days")
	if at < 0 {
		return Reply{}, false
	}
	if g := replyGuard.FindStringSubmatch(content[:at]); g != nil {
		for _, d := range replyDate.FindAllStringSubmatch(g[1], -1) {
			if d[1] == "ge" {
				v.From = unquote(d[2])
			} else {
				v.Until = unquote(d[2])
			}
		}
	}
	body := strings.TrimSuffix(content[at:], "\n")
	if v.dated() {
		body = strings.TrimSuffix(body, "\n}")
	}
	m := vacationLine.FindStringSubmatch(body)
	if m == nil {
		return Reply{}, false
	}
	v.Days, _ = strconv.Atoi(m[1])
	v.Subject = unquote(m[2])
	for _, a := range regexp.MustCompile(quoted).FindAllStringSubmatch(m[3], -1) {
		v.Addresses = append(v.Addresses, unquote(a[1]))
	}
	text, ok := replyText(dotUnstuff(m[4]))
	if !ok {
		return Reply{}, false
	}
	v.Text = text
	return v, v.script() == content
}

func activeIndex(items []Reply, content string) int {
	sent, ok := readReply(content)
	if !ok {
		return -1
	}
	for i, r := range items {
		if r.script() == sent.script() {
			return i
		}
	}
	return -1
}
