package alborzsieve

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
)

// A Rule is what a mail client calls one: tests on a message and what
// to do when they hold. It is written as one Sieve block and read back
// from it; a rule that stops keeps the reader's own script, which runs
// after the composed ones, from seeing the message.
type Rule struct {
	Name       string
	Any        bool // any condition suffices; otherwise all must hold
	Conditions []Condition
	Folder     string // fileinto
	MarkRead   bool
	Star       bool
	Discard    bool
	Forward    string // redirect :copy
	Stop       bool
}

// Condition is one test: a field, how it is matched, and the value.
// From, to and cc test the address part; subject and list, the header.
type Condition struct {
	Field string // from to cc subject list
	Match string // is contains
	Value string
}

// conditionFields and conditionMatches are what the form offers, in
// order; a value outside them is not the page's own.
var (
	conditionFields  = []string{"from", "to", "cc", "subject", "list"}
	conditionMatches = []string{"is", "contains"}
	headerOf         = map[string]string{"from": "from", "to": "to", "cc": "cc", "subject": "subject", "list": "list-id"}
)

func (c Condition) test() string {
	kind := "address"
	if c.Field == "subject" || c.Field == "list" {
		kind = "header"
	}
	return fmt.Sprintf("%s :%s %s %s", kind, c.Match, quote(headerOf[c.Field]), quote(c.Value))
}

func (r Rule) block() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# rule: %s\n", strings.ReplaceAll(r.Name, "\n", " "))
	join := "allof"
	if r.Any {
		join = "anyof"
	}
	tests := make([]string, len(r.Conditions))
	for i, c := range r.Conditions {
		tests[i] = c.test()
	}
	fmt.Fprintf(&b, "if %s(%s) {\n", join, strings.Join(tests, ", "))
	if r.Folder != "" {
		fmt.Fprintf(&b, "    fileinto %s;\n", quote(r.Folder))
	}
	if r.MarkRead {
		b.WriteString("    addflag \"\\\\Seen\";\n")
	}
	if r.Star {
		b.WriteString("    addflag \"\\\\Flagged\";\n")
	}
	if r.Forward != "" {
		fmt.Fprintf(&b, "    redirect :copy %s;\n", quote(r.Forward))
	}
	if r.Discard {
		b.WriteString("    discard;\n")
	}
	if r.Stop {
		b.WriteString("    stop;\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// rulesScriptFor writes the rules, requiring only what they use.
func rulesScriptFor(rules []Rule) string {
	if len(rules) == 0 {
		return ""
	}
	var need []string
	add := func(ext string) {
		for _, n := range need {
			if n == ext {
				return
			}
		}
		need = append(need, ext)
	}
	for _, r := range rules {
		if r.Folder != "" {
			add("fileinto")
		}
		if r.MarkRead || r.Star {
			add("imap4flags")
		}
		if r.Forward != "" {
			add("copy")
		}
	}
	var b strings.Builder
	b.WriteString("# Rules, composed on the Rules page. They run in this order; a rule that stops ends delivery here.\n")
	b.WriteString(requireLine(need))
	for _, r := range rules {
		b.WriteString("\n" + r.block())
	}
	return b.String()
}

var (
	ruleName = regexp.MustCompile(`^# rule: (.*)$`)
	ruleIf   = regexp.MustCompile(`^if (allof|anyof)\((.*)\) \{$`)
	ruleTest = regexp.MustCompile(`(address|header) :(is|contains) ` + quoted + ` ` + quoted)
	ruleAct  = regexp.MustCompile(`^    (fileinto ` + quoted + `|addflag "\\\\(Seen|Flagged)"|redirect :copy ` + quoted + `|discard|stop);$`)
)

// readRules reads the page's own shape back. ok is false for a script
// written by someone else: whatever was read is re-emitted and must
// come out byte for byte the same.
func readRules(content string) (rules []Rule, ok bool) {
	lines := strings.Split(content, "\n")
	for i := 0; i < len(lines); i++ {
		m := ruleName.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		r := Rule{Name: m[1]}
		i++
		if i >= len(lines) {
			return nil, false
		}
		h := ruleIf.FindStringSubmatch(lines[i])
		if h == nil {
			return nil, false
		}
		r.Any = h[1] == "anyof"
		for _, t := range ruleTest.FindAllStringSubmatch(h[2], -1) {
			field := ""
			for f, header := range headerOf {
				if header == unquote(t[3]) {
					field = f
				}
			}
			r.Conditions = append(r.Conditions, Condition{Field: field, Match: t[2], Value: unquote(t[4])})
		}
		for i++; i < len(lines) && lines[i] != "}"; i++ {
			a := ruleAct.FindStringSubmatch(lines[i])
			if a == nil {
				return nil, false
			}
			switch {
			case a[2] != "":
				r.Folder = unquote(a[2])
			case a[3] == "Seen":
				r.MarkRead = true
			case a[3] == "Flagged":
				r.Star = true
			case a[4] != "":
				r.Forward = unquote(a[4])
			case a[1] == "discard":
				r.Discard = true
			case a[1] == "stop":
				r.Stop = true
			}
		}
		rules = append(rules, r)
	}
	return rules, rulesScriptFor(rules) == content
}

type RulesRenderData struct {
	alborz.BaseRenderData
	Rules  []Rule
	Loaded string
	ByHand bool
	Script string
	// What the server lets a rule do; an action it cannot take is not
	// offered.
	CanFile, CanFlag, CanCopy, CanWire bool
	// The form: the rule it holds, its place in the list (-1 when new),
	// the folders to file into, and what the form answers.
	Editing Rule
	Index   int
	Folders []string
	Fields  []string
	Matches []string
	Error   string
	Rail    map[string][]alborz.RailRow
}

func rulesData(ctx *alborz.Context) (*RulesRenderData, error) {
	data := &RulesRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("filters.rules")),
		Script:         rulesScript,
		Index:          -1,
		Fields:         conditionFields,
		Matches:        conditionMatches,
		Rail:           rail(ctx),
	}
	err := ctx.DoSieve(func(c alborz.SieveClient) error {
		data.CanFile = hasExtension(c, "fileinto")
		data.CanFlag = hasExtension(c, "imap4flags")
		data.CanCopy = hasExtension(c, "copy")
		data.CanWire = hasExtension(c, "include")
		content, exists, err := scriptIfAny(c, rulesScript)
		if err != nil || !exists {
			return err
		}
		data.Loaded = fingerprint(content)
		rules, ok := readRules(content)
		data.Rules, data.ByHand = rules, !ok
		return nil
	})
	return data, err
}

func handleRules(ctx *alborz.Context) error {
	data, err := rulesData(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "rules.html", data)
}

// handleRuleForm opens a rule to write: a new one, prefilled from the
// message the link came from, or the one at the URL's place.
func handleRuleForm(ctx *alborz.Context) error {
	data, err := rulesData(ctx)
	if err != nil {
		return err
	}
	if data.Folders, err = alborzbase.MailboxNames(ctx); err != nil {
		return err
	}
	if at := ctx.Param("index"); at != "" {
		i, err := strconv.Atoi(at)
		if err != nil || i < 0 || i >= len(data.Rules) {
			return alborz.NotFoundf("no rule at %s", at)
		}
		data.Editing, data.Index = data.Rules[i], i
	} else {
		data.Editing = Rule{Stop: true}
		for _, field := range []string{"from", "list", "subject"} {
			if v := strings.TrimSpace(ctx.QueryParam(field)); v != "" {
				match := "is"
				if field == "subject" {
					match = "contains"
				}
				data.Editing.Conditions = append(data.Editing.Conditions, Condition{Field: field, Match: match, Value: v})
			}
		}
		if len(data.Editing.Conditions) > 0 {
			data.Editing.Name = data.Editing.Conditions[0].Value
		}
	}
	return ctx.Render(http.StatusOK, "rule-edit.html", data)
}

// ruleFromForm reads the form; the conditions are three rows, a row
// with no value being left out.
func ruleFromForm(ctx *alborz.Context) (Rule, error) {
	r := Rule{
		Name:     strings.TrimSpace(ctx.FormValue("name")),
		Any:      ctx.FormValue("join") == "any",
		Folder:   ctx.FormValue("folder"),
		MarkRead: ctx.FormValue("read") != "",
		Star:     ctx.FormValue("star") != "",
		Discard:  ctx.FormValue("discard") != "",
		Forward:  strings.TrimSpace(ctx.FormValue("forward")),
		Stop:     ctx.FormValue("stop") != "",
	}
	params, err := ctx.FormParams()
	if err != nil {
		return r, err
	}
	for i, value := range params["value"] {
		value = strings.TrimSpace(value)
		if value == "" || i >= len(params["field"]) || i >= len(params["match"]) {
			continue
		}
		c := Condition{Field: params["field"][i], Match: params["match"][i], Value: value}
		if headerOf[c.Field] == "" || (c.Match != "is" && c.Match != "contains") {
			return r, errors.New(ctx.T("filters.badcondition"))
		}
		r.Conditions = append(r.Conditions, c)
	}
	switch {
	case r.Name == "":
		return r, errors.New(ctx.T("form.nameneeded"))
	case len(r.Conditions) == 0:
		return r, errors.New(ctx.T("filters.conditionneeded"))
	case r.Folder == "" && !r.MarkRead && !r.Star && !r.Discard && r.Forward == "":
		return r, errors.New(ctx.T("filters.actionneeded"))
	case r.Forward != "" && !strings.Contains(r.Forward, "@"):
		return r, fmt.Errorf(ctx.T("filters.notanaddress"), r.Forward)
	}
	return r, nil
}

func handleRuleSave(ctx *alborz.Context) error {
	r, err := ruleFromForm(ctx)
	index, _ := strconv.Atoi(ctx.FormValue("index"))
	if ctx.FormValue("index") == "" {
		index = -1
	}
	if err != nil {
		data, derr := rulesData(ctx)
		if derr != nil {
			return derr
		}
		if data.Folders, derr = alborzbase.MailboxNames(ctx); derr != nil {
			return derr
		}
		data.Editing, data.Index, data.Error = r, index, err.Error()
		return ctx.Render(http.StatusUnprocessableEntity, "rule-edit.html", data)
	}
	err = changeRules(ctx, func(rules []Rule) []Rule {
		if index >= 0 && index < len(rules) {
			rules[index] = r
			return rules
		}
		return append(rules, r)
	})
	return answer(ctx, err, "/filters/rules", ctx.T("notice.rulesaved"))
}

func handleRuleDelete(ctx *alborz.Context) error {
	index, err := strconv.Atoi(ctx.FormValue("index"))
	if err != nil {
		return err
	}
	err = changeRules(ctx, func(rules []Rule) []Rule {
		if index < 0 || index >= len(rules) {
			return rules
		}
		return append(rules[:index], rules[index+1:]...)
	})
	return answer(ctx, err, "/filters/rules", ctx.T("notice.ruledeleted"))
}

// handleRuleMove swaps a rule with its neighbour: order is meaning, the
// first rule that stops wins.
func handleRuleMove(ctx *alborz.Context) error {
	index, err := strconv.Atoi(ctx.FormValue("index"))
	if err != nil {
		return err
	}
	to := index - 1
	if ctx.FormValue("dir") == "down" {
		to = index + 1
	}
	err = changeRules(ctx, func(rules []Rule) []Rule {
		if index < 0 || index >= len(rules) || to < 0 || to >= len(rules) {
			return rules
		}
		rules[index], rules[to] = rules[to], rules[index]
		return rules
	})
	return answer(ctx, err, "/filters/rules", ctx.T("notice.rulesaved"))
}

func changeRules(ctx *alborz.Context, change func([]Rule) []Rule) error {
	loaded := ctx.FormValue("loaded")
	return ctx.DoSieve(func(c alborz.SieveClient) error {
		return rewrite(c, rulesScript, loaded, func(current string, exists bool) (string, error) {
			var rules []Rule
			if exists {
				var ok bool
				if rules, ok = readRules(current); !ok {
					return "", errors.New(ctx.T("filters.byhand"))
				}
			}
			return rulesScriptFor(change(rules)), nil
		})
	})
}
