package alborzsieve

import (
	"errors"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
)

// Days a sender waits between two automatic answers (RFC 5230 4.1);
// the default is the extension's own.
const (
	vacationDaysDefault = 7
	vacationDaysMax     = 30
	repliesKey          = "sieve.replies"
	maxReplies          = 20
	dateLayout          = "2006-01-02"
)

// A Reply is one automatic answer the reader has written. Sieve sends
// one per message, so one at a time is composed into the script; the
// rest wait in the account's store, beside the signatures. Addresses
// are the ones the reply answers for: the server replies only to mail
// sent to one of them (RFC 5230 4.5). From and Until bound the days it
// speaks on, through the date extension (RFC 5260); empty is open.
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

func loadReplies(s alborz.Store) (*Replies, error) {
	r := &Replies{}
	if err := s.Get(repliesKey, r); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	return r, nil
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

// replyBoundary separates the two parts of a reply; the text is
// quoted-printable, which cannot contain it.
const replyBoundary = "alborz-reply"

// replyEntity is the reply as a MIME entity (RFC 5230 :mime): the text
// as written, and the HTML alternative that carries its direction, the
// same pair sent mail carries, since Gmail and Outlook lay a bare
// text/plain part out left to right whatever it says. Quoted-printable
// keeps the script readable in the raw editor.
func replyEntity(text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\n\n", replyBoundary)
	for _, part := range []struct{ kind, body string }{{"text/plain", text + "\n"}, {"text/html", alborz.HTMLAlternative(text)}} {
		fmt.Fprintf(&b, "--%s\nContent-Type: %s; charset=utf-8\nContent-Transfer-Encoding: quoted-printable\n\n", replyBoundary, part.kind)
		w := quotedprintable.NewWriter(&b)
		w.Write([]byte(part.body))
		w.Close()
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "--%s--\n", replyBoundary)
	return strings.ReplaceAll(b.String(), "\r\n", "\n")
}

// dotStuff and dotUnstuff are the multi-line string's own rule (RFC 5228
// 2.4.2): a line that begins with a dot is written with two.
func dotStuff(s string) string   { return strings.ReplaceAll("\n"+s, "\n.", "\n..")[1:] }
func dotUnstuff(s string) string { return strings.ReplaceAll("\n"+s, "\n..", "\n.")[1:] }

// replyText reads the text part of the entity back.
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

// readReply reads the page's own shape back: the name it wrote, the
// dates it guarded with, and the vacation command itself. ok is false
// for a script written by someone else.
func readReply(content string) (v Reply, ok bool) {
	if m := replyName.FindStringSubmatch(content); m != nil {
		v.Name = m[1]
	}
	// The command runs to the end of the script, its entity included,
	// so only what comes before it is read line by line.
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

type RepliesRenderData struct {
	alborz.BaseRenderData
	Replies []Reply
	// Active is the place in the list of the reply the script sends,
	// -1 when none; ByHand says a script exists that the page did not
	// write, and Script names it.
	Active int
	Loaded string
	ByHand bool
	Script string
	// The form: the reply it holds and its place (-1 when new).
	Editing    Reply
	Index      int
	Identities []string
	CanReply   bool
	CanDate    bool
	CanWire    bool
	Error      string
	Rail       map[string][]alborz.RailRow
}

func repliesData(ctx *alborz.Context) (*RepliesRenderData, error) {
	replies, err := loadReplies(ctx.Session.Store())
	if err != nil {
		return nil, err
	}
	settings, err := alborzbase.LoadSettings(ctx.Session.Store())
	if err != nil {
		return nil, err
	}
	data := &RepliesRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("filters.autoreply")),
		Replies:        replies.Items,
		Active:         -1,
		Index:          -1,
		Script:         vacationScript,
		Identities:     append([]string{ctx.Session.Username()}, settings.Identities...),
		Rail:           rail(ctx),
	}
	err = ctx.DoSieve(func(c alborz.SieveClient) error {
		data.CanReply = hasExtension(c, "vacation")
		data.CanDate = hasExtension(c, "date") && hasExtension(c, "relational")
		data.CanWire = hasExtension(c, "include")
		content, exists, err := scriptIfAny(c, vacationScript)
		if err != nil || !exists {
			return err
		}
		data.Loaded = fingerprint(content)
		_, ok := readReply(content)
		data.ByHand, data.Active = !ok, activeIndex(replies.Items, content)
		return nil
	})
	return data, err
}

func handleReplies(ctx *alborz.Context) error {
	data, err := repliesData(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "autoreply.html", data)
}

func handleReplyForm(ctx *alborz.Context) error {
	data, err := repliesData(ctx)
	if err != nil {
		return err
	}
	data.Editing = Reply{Days: vacationDaysDefault, Addresses: data.Identities}
	if at := ctx.Param("index"); at != "" {
		i, err := strconv.Atoi(at)
		if err != nil || i < 0 || i >= len(data.Replies) {
			return alborz.NotFoundf("no reply at %s", at)
		}
		data.Editing, data.Index = data.Replies[i], i
	}
	return ctx.Render(http.StatusOK, "autoreply-edit.html", data)
}

func replyFromForm(ctx *alborz.Context) (Reply, error) {
	params, err := ctx.FormParams()
	if err != nil {
		return Reply{}, err
	}
	v := Reply{
		Name:      strings.TrimSpace(ctx.FormValue("name")),
		Subject:   strings.TrimSpace(ctx.FormValue("subject")),
		Text:      strings.TrimRight(strings.ReplaceAll(ctx.FormValue("text"), "\r\n", "\n"), "\n"),
		Addresses: params["addresses"],
		From:      strings.TrimSpace(ctx.FormValue("from")),
		Until:     strings.TrimSpace(ctx.FormValue("until")),
	}
	v.Days, err = alborz.ReadInt(ctx.FormValue("days"))
	switch {
	case v.Name == "":
		return v, errors.New(ctx.T("form.nameneeded"))
	case v.Text == "":
		return v, errors.New(ctx.T("filters.replyneeded"))
	case err != nil || v.Days < 1 || v.Days > vacationDaysMax:
		return v, fmt.Errorf(ctx.T("filters.baddays"), vacationDaysMax)
	}
	for _, bound := range []*string{&v.From, &v.Until} {
		if *bound == "" {
			continue
		}
		day, err := ctx.ReadDate(*bound, time.UTC)
		if err != nil {
			return v, errors.New(ctx.T("filters.baddate"))
		}
		*bound = day.Format(dateLayout)
	}
	if v.From != "" && v.Until != "" && v.Until < v.From {
		return v, errors.New(ctx.T("filters.baddate"))
	}
	return v, nil
}

func handleReplySave(ctx *alborz.Context) error {
	v, err := replyFromForm(ctx)
	index := -1
	if at := ctx.FormValue("index"); at != "" {
		index, _ = strconv.Atoi(at)
	}
	if err != nil {
		data, derr := repliesData(ctx)
		if derr != nil {
			return derr
		}
		data.Editing, data.Index, data.Error = v, index, err.Error()
		return ctx.Render(http.StatusUnprocessableEntity, "autoreply-edit.html", data)
	}
	replies, err := loadReplies(ctx.Session.Store())
	if err != nil {
		return err
	}
	was := append([]Reply(nil), replies.Items...)
	if index >= 0 && index < len(replies.Items) {
		replies.Items[index] = v
	} else if len(replies.Items) >= maxReplies {
		return answer(ctx, fmt.Errorf(ctx.T("filters.toomanyreplies"), maxReplies), "/filters/autoreply", "")
	} else {
		replies.Items = append(replies.Items, v)
	}
	if err := ctx.Session.Store().Put(repliesKey, replies); err != nil {
		return err
	}
	// An edit to the reply being sent goes out at once; any other is
	// kept for activating, and the notice says which happened.
	sent := false
	loaded := ctx.FormValue("loaded")
	if loaded != "" && index >= 0 {
		err = ctx.DoSieve(func(c alborz.SieveClient) error {
			return rewrite(c, vacationScript, loaded, func(current string, exists bool) (string, error) {
				if activeIndex(was, current) != index {
					return current, nil
				}
				sent = true
				return v.script(), nil
			})
		})
	}
	saved := ctx.T("notice.autoreplykept")
	if sent {
		saved = ctx.T("notice.autoreplysaved")
	}
	return answer(ctx, err, "/filters/autoreply", saved)
}

// activeIndex is the place of the reply the script sends, -1 when none
// of the list.
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

func handleReplyDelete(ctx *alborz.Context) error {
	index, err := strconv.Atoi(ctx.FormValue("index"))
	if err != nil {
		return err
	}
	replies, err := loadReplies(ctx.Session.Store())
	if err != nil {
		return err
	}
	if index < 0 || index >= len(replies.Items) {
		return alborz.NotFoundf("no reply at %d", index)
	}
	gone := replies.Items[index]
	replies.Items = append(replies.Items[:index], replies.Items[index+1:]...)
	if err := ctx.Session.Store().Put(repliesKey, replies); err != nil {
		return err
	}
	// Deleting the reply being sent stops it.
	loaded := ctx.FormValue("loaded")
	if loaded != "" {
		err = ctx.DoSieve(func(c alborz.SieveClient) error {
			return rewrite(c, vacationScript, loaded, func(current string, exists bool) (string, error) {
				if sent, ok := readReply(current); ok && sent.script() == gone.script() {
					return "", nil
				}
				return current, nil
			})
		})
	}
	return answer(ctx, err, "/filters/autoreply", ctx.T("notice.autoreplydeleted"))
}

// handleReplyActivate composes the chosen reply into the script; an
// empty index composes none, which deletes it.
func handleReplyActivate(ctx *alborz.Context) error {
	replies, err := loadReplies(ctx.Session.Store())
	if err != nil {
		return err
	}
	next := ""
	saved := ctx.T("notice.autoreplyoff")
	if at := ctx.FormValue("index"); at != "" {
		index, err := strconv.Atoi(at)
		if err != nil || index < 0 || index >= len(replies.Items) {
			return alborz.NotFoundf("no reply at %s", at)
		}
		next, saved = replies.Items[index].script(), ctx.T("notice.autoreplyon")
	}
	loaded := ctx.FormValue("loaded")
	err = ctx.DoSieve(func(c alborz.SieveClient) error {
		return rewrite(c, vacationScript, loaded, func(current string, exists bool) (string, error) {
			if exists {
				if _, ok := readReply(current); !ok {
					return "", errors.New(ctx.T("filters.byhand"))
				}
			}
			return next, nil
		})
	})
	return answer(ctx, err, "/filters/autoreply", saved)
}
