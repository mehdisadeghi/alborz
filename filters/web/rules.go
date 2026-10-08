package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/filters"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
)

type RulesRenderData struct {
	sievePageData
	Rules []filters.Rule
	// Shown are the places in Rules of the rules one page shows.
	Shown []int
	Pager web.Pager

	CanFile, CanFlag, CanCopy bool

	Editing filters.Rule
	Index   int
	Folders []string
	Fields  []string
	Matches []string
}

func rulesData(ctx *web.Context) (*RulesRenderData, error) {
	data := &RulesRenderData{
		sievePageData: sievePage(ctx, ctx.T("filters.rules"), filters.RulesScript),
		Index:         -1,
		Fields:        filters.ConditionFields,
		Matches:       filters.ConditionMatches,
	}
	err := ctx.DoSieve(func(c alborz.SieveClient) error {
		data.CanFile = filters.HasExtension(c, "fileinto")
		data.CanFlag = filters.HasExtension(c, "imap4flags")
		data.CanCopy = filters.HasExtension(c, "copy")
		data.CanWire = filters.HasExtension(c, "include")
		var err error
		data.Rules, data.Loaded, data.ByHand, err = filters.LoadRules(c)
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
	if data.Folders, err = mail.MailboxNames(ctx.Request().Context(), ctx.Login); err != nil {
		return err
	}
	if at := ctx.Param("index"); at != "" {
		i, err := strconv.Atoi(at)
		if err != nil || i < 0 || i >= len(data.Rules) {
			return alborz.NotFound("notfound.rule")
		}
		data.Editing, data.Index = data.Rules[i], i
	} else {
		data.Editing = filters.Rule{Stop: true}
		for _, field := range []string{"from", "list", "subject"} {
			if v := strings.TrimSpace(ctx.QueryParam(field)); v != "" {
				match := "is"
				if field == "subject" {
					match = "contains"
				}
				data.Editing.Conditions = append(data.Editing.Conditions, filters.Condition{Field: field, Match: match, Value: v})
			}
		}
		if len(data.Editing.Conditions) > 0 {
			data.Editing.Name = data.Editing.Conditions[0].Value
		}
	}
	return ctx.Render(http.StatusOK, "filters/rule-edit", data)
}

func ruleFromForm(ctx *web.Context) (filters.Rule, error) {
	r := filters.Rule{
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
		c := filters.Condition{Field: params["field"][i], Match: params["match"][i], Value: value}
		if !slices.Contains(filters.ConditionFields, c.Field) || (c.Match != "is" && c.Match != "contains") {
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
	index := -1
	if ctx.FormValue("index") != "" {
		var err error
		if index, err = formIndex(ctx); err != nil {
			return err
		}
	}
	r, err := ruleFromForm(ctx)
	if err != nil {
		data, derr := rulesData(ctx)
		if derr != nil {
			return derr
		}
		if data.Folders, derr = mail.MailboxNames(ctx.Request().Context(), ctx.Login); derr != nil {
			return derr
		}
		data.Editing, data.Index = r, index
		return web.RenderRefused(ctx, "filters/rule-edit", data, err.Error())
	}
	made := -1
	err = changeRules(ctx, func(rules []filters.Rule) []filters.Rule {
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
		return ctx.FoundRedirect(ctx.URL("filters.rules"))
	}
	return answer(ctx, err, "filters.rules", ctx.T("notice.rulesaved"))
}

func handleBlock(ctx *web.Context) error {
	value := strings.ToLower(strings.TrimSpace(ctx.FormValue("value")))
	domain := ctx.FormValue("domain") != ""
	if value == "" || (domain && strings.ContainsRune(value, '@')) || (!domain && !strings.ContainsRune(value, '@')) {
		return web.NewError(http.StatusBadRequest, "nothing to block")
	}
	junk, err := mail.RoleFolder(ctx.Request().Context(), ctx.Login, "junk")
	if err != nil {
		return err
	}
	if junk == "" {
		return ctx.NotifyRedirect(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("filters.nojunk")}, ctx.NextOr(ctx.URL("filters.rules")))
	}
	err = ctx.DoSieve(func(c alborz.SieveClient) error {
		return filters.Block(c, value, domain, junk)
	})
	return answer(ctx, err, "filters.rules", fmt.Sprintf(ctx.T("notice.blocked"), value))
}

func handleRuleDelete(ctx *web.Context) error {
	index, err := formIndex(ctx)
	if err != nil {
		return err
	}
	return answer(ctx, changeRules(ctx, func(rules []filters.Rule) []filters.Rule {
		if index < 0 || index >= len(rules) {
			return rules
		}
		return append(rules[:index], rules[index+1:]...)
	}), "filters.rules", ctx.T("notice.ruledeleted"))
}

// Order is meaning: the first rule that stops wins.
func handleRuleMove(ctx *web.Context) error {
	index, err := formIndex(ctx)
	if err != nil {
		return err
	}
	to := index - 1
	if ctx.FormValue("dir") == "down" {
		to = index + 1
	}
	return answer(ctx, changeRules(ctx, func(rules []filters.Rule) []filters.Rule {
		if index < 0 || index >= len(rules) || to < 0 || to >= len(rules) {
			return rules
		}
		rules[index], rules[to] = rules[to], rules[index]
		return rules
	}), "filters.rules", ctx.T("notice.rulesaved"))
}

func handleRuleToggle(ctx *web.Context) error {
	index, err := formIndex(ctx)
	if err != nil {
		return err
	}
	return answer(ctx, changeRules(ctx, func(rules []filters.Rule) []filters.Rule {
		if index >= 0 && index < len(rules) {
			rules[index].Off = !rules[index].Off
		}
		return rules
	}), "filters.rules", ctx.T("notice.rulesaved"))
}

// formIndex is the row a form names by its index; one that is no number is refused.
func formIndex(ctx *web.Context) (int, error) {
	i, err := strconv.Atoi(ctx.FormValue("index"))
	if err != nil {
		return 0, web.NewError(http.StatusBadRequest, err)
	}
	return i, nil
}

func changeRules(ctx *web.Context, change func([]filters.Rule) []filters.Rule) error {
	return ctx.DoSieve(func(c alborz.SieveClient) error {
		return filters.ChangeRules(c, ctx.FormValue("loaded"), change)
	})
}
