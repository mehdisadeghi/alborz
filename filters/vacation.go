package filters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/dates"
	"git.mehdix.org/alborz/internal/lang"
	"git.mehdix.org/alborz/internal/locale"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
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

func SaveReplies(ctx context.Context, s alborz.Store, r *Replies) error {
	return s.Put(ctx, repliesKey, r)
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
	content, exists, err := scriptIfAny(c, VacationScript)
	if err != nil || !exists {
		return -1, "", false, err
	}
	_, ok := readReply(content)
	return activeIndex(items, content), Fingerprint(content), !ok, nil
}

// ReplaceReply sends v at once if the reply at index in was is the one being sent.
func ReplaceReply(c alborz.SieveClient, loaded string, was []Reply, index int, v Reply) (sent bool, err error) {
	err = rewrite(c, VacationScript, loaded, func(current string, exists bool) (string, error) {
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
	return rewrite(c, VacationScript, loaded, func(current string, exists bool) (string, error) {
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
		w.Write([]byte(part.body))
		w.Close()
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "--%s--\n", replyBoundary)
	return strings.ReplaceAll(b.String(), "\r\n", "\n")
}

// dotStuff and dotUnstuff apply the multi-line string rule of RFC 5228 2.4.2.
func dotStuff(s string) string   { return strings.ReplaceAll("\n"+s, "\n.", "\n..")[1:] }
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

type RepliesRenderData struct {
	web.BaseRenderData
	Replies []Reply
	// Active is the index of the reply the script sends, -1 when none.
	Active     int
	Loaded     string
	ByHand     bool
	Script     string
	Editing    Reply
	Index      int
	Identities []string
	CanReply   bool
	CanDate    bool
	CanWire    bool
	Rail       map[string][]web.RailRow

	// From and Until are Editing's date fields as shown.
	From, Until dates.Field
}

func repliesData(ctx *web.Context) (*RepliesRenderData, error) {
	replies, err := LoadReplies(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return nil, err
	}
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return nil, err
	}
	data := &RepliesRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("filters.autoreply")),
		Replies:        replies.Items,
		Active:         -1,
		Index:          -1,
		Script:         VacationScript,
		Identities:     append([]string{ctx.Login.Username()}, settings.Identities...),
		Rail:           rail(ctx),
	}
	err = ctx.DoSieve(func(c alborz.SieveClient) error {
		data.CanReply = HasExtension(c, "vacation")
		data.CanDate = HasExtension(c, "date") && HasExtension(c, "relational")
		data.CanWire = HasExtension(c, "include")
		var err error
		data.Active, data.Loaded, data.ByHand, err = ActiveReply(c, replies.Items)
		return err
	})
	return data, err
}

func handleReplies(ctx *web.Context) error {
	data, err := repliesData(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "filters/autoreply", data)
}

func handleReplyForm(ctx *web.Context) error {
	data, err := repliesData(ctx)
	if err != nil {
		return err
	}
	data.Editing = Reply{Days: ReplyDays, Addresses: data.Identities}
	if at := ctx.Param("index"); at != "" {
		i, err := strconv.Atoi(at)
		if err != nil || i < 0 || i >= len(data.Replies) {
			return alborz.NotFound("notfound.reply")
		}
		data.Editing, data.Index = data.Replies[i], i
	}
	data.From, data.Until = ctx.Dates().Day(data.Editing.From), ctx.Dates().Day(data.Editing.Until)
	return ctx.Render(http.StatusOK, "filters/autoreply-edit", data)
}

func replyFromForm(ctx *web.Context) (Reply, error) {
	params, err := ctx.FormParams()
	if err != nil {
		return Reply{}, err
	}
	v := Reply{
		Name:      strings.TrimSpace(ctx.FormValue("name")),
		Subject:   strings.TrimSpace(ctx.FormValue("subject")),
		Text:      strings.TrimRight(strings.ReplaceAll(ctx.FormValue("text"), "\r\n", "\n"), "\n"),
		Addresses: params["addresses"],
	}
	v.Days, err = locale.ReadInt(ctx.FormValue("days"))
	switch {
	case v.Name == "":
		return v, errors.New(ctx.T("form.nameneeded"))
	case v.Text == "":
		return v, errors.New(ctx.T("filters.replyneeded"))
	case err != nil || v.Days < 1 || v.Days > MaxReplyDays:
		return v, fmt.Errorf(ctx.T("filters.baddays"), MaxReplyDays)
	}
	for name, bound := range map[string]*string{"from": &v.From, "until": &v.Until} {
		field := ctx.DateField(name)
		if field.Value == "" {
			continue
		}
		day, err := field.Read(time.UTC)
		if err != nil {
			return v, errors.New(ctx.T("filters.baddate"))
		}
		*bound = day.Format(time.DateOnly)
	}
	if v.From != "" && v.Until != "" && v.Until < v.From {
		return v, errors.New(ctx.T("filters.baddate"))
	}
	return v, nil
}

func handleReplySave(ctx *web.Context) error {
	v, err := replyFromForm(ctx)
	index := -1
	if at := ctx.FormValue("index"); at != "" {
		index, _ = strconv.Atoi(at)
	}
	refuse := func(message string) error {
		data, err := repliesData(ctx)
		if err != nil {
			return err
		}
		data.Editing, data.Index = v, index
		data.From, data.Until = ctx.DateField("from"), ctx.DateField("until")
		data.Refused(message)
		return ctx.Render(http.StatusUnprocessableEntity, "filters/autoreply-edit", data)
	}
	if err != nil {
		return refuse(err.Error())
	}
	replies, err := LoadReplies(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	was := slices.Clone(replies.Items)
	made, err := replies.Put(index, v)
	if errors.Is(err, ErrTooManyReplies) {
		return answer(ctx, fmt.Errorf(ctx.T("filters.toomanyreplies"), MaxReplies), "filters.autoreply", "")
	}
	if err := SaveReplies(ctx.Request().Context(), ctx.Login.Store(), replies); err != nil {
		return refuse(ctx.SaveRefusal(err))
	}
	// An edit to the reply being sent goes out at once; any other waits for activating.
	sent := false
	loaded := ctx.FormValue("loaded")
	if loaded != "" && index >= 0 {
		err = ctx.DoSieve(func(c alborz.SieveClient) error {
			var err error
			sent, err = ReplaceReply(c, loaded, was, index, v)
			return err
		})
	}
	if err == nil && made >= 0 {
		ctx.Made(ctx.T("notice.autoreplycreated"), v.Name,
			ctx.URL("filters.autoreply.reply", made),
			ctx.URL("filters.autoreply.create"))
		return ctx.Redirect(http.StatusFound, ctx.URL("filters.autoreply"))
	}
	saved := ctx.T("notice.autoreplyinactive")
	if sent {
		saved = ctx.T("notice.autoreplysaved")
	}
	return answer(ctx, err, "filters.autoreply", saved)
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

func handleReplyDelete(ctx *web.Context) error {
	index, err := strconv.Atoi(ctx.FormValue("index"))
	if err != nil {
		return err
	}
	replies, err := LoadReplies(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	if index < 0 || index >= len(replies.Items) {
		return alborz.NotFound("notfound.reply")
	}
	gone := replies.Items[index]
	replies.Items = append(replies.Items[:index], replies.Items[index+1:]...)
	if err := SaveReplies(ctx.Request().Context(), ctx.Login.Store(), replies); err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.SaveRefusal(err)})
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.URL("filters.autoreply")))
	}
	// Deleting the reply being sent stops it.
	loaded := ctx.FormValue("loaded")
	if loaded != "" {
		err = ctx.DoSieve(func(c alborz.SieveClient) error {
			return WithdrawReply(c, loaded, gone)
		})
	}
	return answer(ctx, err, "filters.autoreply", ctx.T("notice.autoreplydeleted"))
}

// An empty index composes no reply, which deletes the script.
func handleReplyActivate(ctx *web.Context) error {
	replies, err := LoadReplies(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	loaded := ctx.FormValue("loaded")
	at := ctx.FormValue("index")
	if at == "" {
		err = ctx.DoSieve(func(c alborz.SieveClient) error {
			return DeactivateReply(c, loaded)
		})
		return answer(ctx, err, "filters.autoreply", ctx.T("notice.autoreplyoff"))
	}
	index, err := strconv.Atoi(at)
	if err != nil || index < 0 || index >= len(replies.Items) {
		return alborz.NotFound("notfound.reply")
	}
	err = ctx.DoSieve(func(c alborz.SieveClient) error {
		return ActivateReply(c, loaded, replies.Items[index])
	})
	return answer(ctx, err, "filters.autoreply", ctx.T("notice.autoreplyon"))
}
