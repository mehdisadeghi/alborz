package filters

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
)

// Rule is one Sieve block; Stop also hides the message from the reader's own script, which runs after.
type Rule struct {
	Name       string
	Any        bool
	Conditions []Condition
	Folder     string
	MarkRead   bool
	Star       bool
	Discard    bool
	Forward    string
	Stop       bool
	// Off keeps the rule in the script under a test that never holds.
	Off bool
}

// Condition is one test: from, to and cc test the address part, subject and list the header.
type Condition struct {
	Field string
	Match string
	Value string
}

var (
	ConditionFields  = []string{"from", "to", "cc", "subject", "list"}
	ConditionMatches = []string{"is", "contains", "domain"}
	headerOf         = map[string]string{"from": "from", "to": "to", "cc": "cc", "subject": "subject", "list": "list-id"}
)

func (c Condition) test() string {
	kind := "address"
	if c.Field == "subject" || c.Field == "list" {
		kind = "header"
	}
	// "domain" matches the address's domain part whole (RFC 5228 2.7.4).
	if c.Match == "domain" {
		return fmt.Sprintf("address :domain :is %s %s", quote(headerOf[c.Field]), quote(c.Value))
	}
	return fmt.Sprintf("%s :%s %s %s", kind, c.Match, quote(headerOf[c.Field]), quote(c.Value))
}

func blockRule(value string, domain bool, junk string) Rule {
	match := "is"
	if domain {
		match = "domain"
	}
	return Rule{Name: value, Conditions: []Condition{{Field: "from", Match: match, Value: value}}, Folder: junk, MarkRead: true, Stop: true}
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
	test := fmt.Sprintf("%s(%s)", join, strings.Join(tests, ", "))
	if r.Off {
		test = "allof(false, " + test + ")"
	}
	fmt.Fprintf(&b, "if %s {\n", test)
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
		b.WriteString("\n")
		b.WriteString(r.block())
	}
	return b.String()
}

// offPrefix switches a rule off: allof(false, ...) never holds yet keeps its tests.
const offPrefix = "if allof(false, "

var (
	ruleName = regexp.MustCompile(`^# rule: (.*)$`)
	ruleIf   = regexp.MustCompile(`^if (allof|anyof)\((.*)\) \{$`)
	ruleTest = regexp.MustCompile(`(address|header) :(is|contains|domain :is) ` + quoted + ` ` + quoted)
	ruleAct  = regexp.MustCompile(`^    (fileinto ` + quoted + `|addflag "\\\\(Seen|Flagged)"|redirect :copy ` + quoted + `|discard|stop);$`)
)

// readRules returns ok false unless re-emitting what it read reproduces the script exactly.
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
		line := lines[i]
		if strings.HasPrefix(line, offPrefix) && strings.HasSuffix(line, ")) {") {
			r.Off = true
			line = "if " + strings.TrimSuffix(strings.TrimPrefix(line, offPrefix), ")) {") + ") {"
		}
		h := ruleIf.FindStringSubmatch(line)
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
			match := t[2]
			if match == "domain :is" {
				match = "domain"
			}
			r.Conditions = append(r.Conditions, Condition{Field: field, Match: match, Value: unquote(t[4])})
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

// LoadRules reads the rules script; byHand says it is in a shape the page did not write.
func LoadRules(c alborz.SieveClient) (rules []Rule, loaded string, byHand bool, err error) {
	content, exists, err := scriptIfAny(c, RulesScript)
	if err != nil || !exists {
		return nil, "", false, err
	}
	rules, ok := readRules(content)
	return rules, Fingerprint(content), !ok, nil
}

// ChangeRules rewrites the rules as loaded; ErrByHand leaves a script the page did not write alone.
func ChangeRules(c alborz.SieveClient, loaded string, change func([]Rule) []Rule) error {
	return rewrite(c, RulesScript, loaded, func(current string, exists bool) (string, error) {
		var rules []Rule
		if exists {
			var ok bool
			if rules, ok = readRules(current); !ok {
				return "", ErrByHand
			}
		}
		return rulesScriptFor(change(rules)), nil
	})
}

// Block files mail from an address, or a whole domain, as junk, once.
// No editing session precedes it, and appending cannot undo anyone's edit.
func Block(c alborz.SieveClient, value string, domain bool, junk string) error {
	current, exists, err := scriptIfAny(c, RulesScript)
	if err != nil {
		return err
	}
	loaded := ""
	if exists {
		loaded = Fingerprint(current)
	}
	return rewrite(c, RulesScript, loaded, func(current string, exists bool) (string, error) {
		var rules []Rule
		if exists {
			var ok bool
			if rules, ok = readRules(current); !ok {
				return "", ErrByHand
			}
		}
		block := blockRule(value, domain, junk)
		for _, r := range rules {
			if len(r.Conditions) == 1 && r.Conditions[0] == block.Conditions[0] {
				return rulesScriptFor(rules), nil
			}
		}
		return rulesScriptFor(append(rules, block)), nil
	})
}

type RulesRenderData struct {
	web.BaseRenderData
	Rules []Rule
	// Shown are the places in Rules of the rules one page shows.
	Shown  []int
	Pager  web.Pager
	Loaded string
	ByHand bool
	Script string

	CanFile, CanFlag, CanCopy, CanWire bool

	Editing Rule
	Index   int
	Folders []string
	Fields  []string
	Matches []string
	Rail    map[string][]web.RailRow
}

func rulesData(ctx *web.Context) (*RulesRenderData, error) {
	data := &RulesRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("filters.rules")),
		Script:         RulesScript,
		Index:          -1,
		Fields:         ConditionFields,
		Matches:        ConditionMatches,
		Rail:           rail(ctx),
	}
	err := ctx.DoSieve(func(c alborz.SieveClient) error {
		data.CanFile = HasExtension(c, "fileinto")
		data.CanFlag = HasExtension(c, "imap4flags")
		data.CanCopy = HasExtension(c, "copy")
		data.CanWire = HasExtension(c, "include")
		var err error
		data.Rules, data.Loaded, data.ByHand, err = LoadRules(c)
		return err
	})
	return data, err
}

func handleRules(ctx *web.Context) error {
	data, err := rulesData(ctx)
	if err != nil {
		return err
	}
	at := make([]int, len(data.Rules))
	for i := range at {
		at[i] = i
	}
	if data.Shown, data.Pager, err = web.Paginate(ctx, at); err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "filters/rules", data)
}

func handleRuleForm(ctx *web.Context) error {
	data, err := rulesData(ctx)
	if err != nil {
		return err
	}
	if data.Folders, err = mail.MailboxNames(ctx); err != nil {
		return err
	}
	if at := ctx.Param("index"); at != "" {
		i, err := strconv.Atoi(at)
		if err != nil || i < 0 || i >= len(data.Rules) {
			return alborz.NotFound("notfound.rule")
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
	return ctx.Render(http.StatusOK, "filters/rule-edit", data)
}

func ruleFromForm(ctx *web.Context) (Rule, error) {
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
		if !slices.Contains(ConditionFields, c.Field) || (c.Match != "is" && c.Match != "contains") {
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

func handleRuleSave(ctx *web.Context) error {
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
		if data.Folders, derr = mail.MailboxNames(ctx); derr != nil {
			return derr
		}
		data.Editing, data.Index = r, index
		data.Refused(err.Error())
		return ctx.Render(http.StatusUnprocessableEntity, "filters/rule-edit", data)
	}
	made := -1
	err = changeRules(ctx, func(rules []Rule) []Rule {
		if index >= 0 && index < len(rules) {
			r.Off = rules[index].Off
			rules[index] = r
			return rules
		}
		made = len(rules)
		return append(rules, r)
	})
	if err == nil && made >= 0 {
		ctx.Made(ctx.T("notice.rulecreated"), r.Name,
			ctx.URL("filters.rule", made),
			ctx.URL("filters.rules.create"))
		return ctx.Redirect(http.StatusFound, ctx.URL("filters.rules"))
	}
	return answer(ctx, err, "filters.rules", ctx.T("notice.rulesaved"))
}

func handleBlock(ctx *web.Context) error {
	value := strings.ToLower(strings.TrimSpace(ctx.FormValue("value")))
	domain := ctx.FormValue("domain") != ""
	if value == "" || (domain && strings.ContainsRune(value, '@')) || (!domain && !strings.ContainsRune(value, '@')) {
		return web.NewError(http.StatusBadRequest, "nothing to block")
	}
	junk, err := mail.RoleFolder(ctx, "junk")
	if err != nil {
		return err
	}
	if junk == "" {
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("filters.nojunk")})
		return ctx.Redirect(http.StatusFound, ctx.FormValue("next"))
	}
	err = ctx.DoSieve(func(c alborz.SieveClient) error {
		return Block(c, value, domain, junk)
	})
	return answer(ctx, err, "filters.rules", fmt.Sprintf(ctx.T("notice.blocked"), value))
}

func handleRuleDelete(ctx *web.Context) error {
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
	return answer(ctx, err, "filters.rules", ctx.T("notice.ruledeleted"))
}

// Order is meaning: the first rule that stops wins.
func handleRuleMove(ctx *web.Context) error {
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
	return answer(ctx, err, "filters.rules", ctx.T("notice.rulesaved"))
}

func handleRuleToggle(ctx *web.Context) error {
	index, err := strconv.Atoi(ctx.FormValue("index"))
	if err != nil {
		return err
	}
	err = changeRules(ctx, func(rules []Rule) []Rule {
		if index >= 0 && index < len(rules) {
			rules[index].Off = !rules[index].Off
		}
		return rules
	})
	return answer(ctx, err, "filters.rules", ctx.T("notice.rulesaved"))
}

func changeRules(ctx *web.Context, change func([]Rule) []Rule) error {
	return ctx.DoSieve(func(c alborz.SieveClient) error {
		return ChangeRules(c, ctx.FormValue("loaded"), change)
	})
}
