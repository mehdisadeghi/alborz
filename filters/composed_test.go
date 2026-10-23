package filters

import (
	"errors"
	"maps"
	"strings"
	"testing"

	"git.mehdix.org/alborz"
)

type scriptStore struct {
	scripts map[string]string
	active  string
	writes  int
}

func (s *scriptStore) ListScripts() ([]alborz.SieveScript, error) {
	var out []alborz.SieveScript
	for name := range s.scripts {
		out = append(out, alborz.SieveScript{Name: name, Active: name == s.active})
	}
	return out, nil
}
func (s *scriptStore) GetScript(name string) (string, error) { return s.scripts[name], nil }
func (s *scriptStore) PutScript(name, body string) (string, error) {
	s.writes++
	s.scripts[name] = body
	return "", nil
}
func (s *scriptStore) DeleteScript(name string) error {
	s.writes++
	delete(s.scripts, name)
	return nil
}
func (s *scriptStore) ActivateScript(name string) error { s.writes++; s.active = name; return nil }
func (*scriptStore) Implementation() string             { return "test" }
func (*scriptStore) Extensions() []string               { return []string{"include", "fileinto", "imap4flags"} }
func (*scriptStore) Close() error                       { return nil }

func TestComposedRulesPreserveCustomMainScript(t *testing.T) {
	for _, active := range []string{mainScript, "custom", ""} {
		for _, remove := range []bool{false, true} {
			t.Run(active+map[bool]string{false: "/enable", true: "/remove"}[remove], func(t *testing.T) {
				s := &scriptStore{scripts: map[string]string{mainScript: "# Mine\nkeep;\n", "custom": "keep;\n"}, active: active}
				loaded := ""
				if remove {
					s.scripts[RulesScript] = rulesScriptFor([]Rule{{Name: "rule", MarkRead: true}})
					loaded = Fingerprint(s.scripts[RulesScript])
				}
				before := maps.Clone(s.scripts)
				err := ChangeRules(s, loaded, func([]Rule) []Rule {
					if remove {
						return nil
					}
					return []Rule{{Name: "rule", MarkRead: true}}
				})
				if !errors.Is(err, ErrByHand) {
					t.Errorf("got %v, want ErrByHand", err)
				}
				if s.writes != 0 || !maps.Equal(before, s.scripts) || s.active != active {
					t.Errorf("custom script mutated: writes=%d scripts=%v active=%q", s.writes, s.scripts, s.active)
				}
			})
		}
	}
}

func TestComposedRulesRestoreOriginalActiveScript(t *testing.T) {
	s := &scriptStore{scripts: map[string]string{"custom": "keep;\n"}, active: "custom"}
	if err := ChangeRules(s, "", func([]Rule) []Rule { return []Rule{{Name: "rule", MarkRead: true}} }); err != nil {
		t.Fatal(err)
	}
	if s.active != mainScript || !strings.Contains(s.scripts[mainScript], `include :personal "custom";`) {
		t.Fatalf("not composed: %+v", s)
	}
	if err := ChangeRules(s, Fingerprint(s.scripts[RulesScript]), func([]Rule) []Rule { return nil }); err != nil {
		t.Fatal(err)
	}
	if s.active != "custom" || len(s.scripts) != 1 {
		t.Errorf("not restored: %+v", s)
	}
}

func TestComposedRulesPreserveCustomizedIncludeOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		includes []string
	}{
		{"reordered", []string{ForwardScript, RulesScript}},
		{"duplicated", []string{RulesScript, RulesScript, ForwardScript}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &scriptStore{scripts: map[string]string{
				mainScript:    mainScriptFor(tc.includes, "custom"),
				RulesScript:   rulesScriptFor([]Rule{{Name: "rule", MarkRead: true}}),
				ForwardScript: "keep;\n",
				"custom":      "keep;\n",
			}, active: mainScript}
			before := maps.Clone(s.scripts)
			err := ChangeRules(s, Fingerprint(s.scripts[RulesScript]), func(rules []Rule) []Rule {
				rules[0].Star = true
				return rules
			})
			if !errors.Is(err, ErrByHand) {
				t.Errorf("got %v, want ErrByHand", err)
			}
			if s.writes != 0 || !maps.Equal(before, s.scripts) || s.active != mainScript {
				t.Errorf("custom include order mutated: writes=%d scripts=%v active=%q", s.writes, s.scripts, s.active)
			}
		})
	}
}

func TestRuleFlagsPrecedeFileinto(t *testing.T) {
	r := Rule{Name: "file", Folder: "Archive", MarkRead: true, Star: true}
	block := r.block()
	for _, flag := range []string{"Seen", "Flagged"} {
		if strings.Index(block, flag) > strings.Index(block, "fileinto") {
			t.Errorf("%s applied after fileinto:\n%s", flag, block)
		}
	}
	if _, ok := readRules(rulesScriptFor([]Rule{r})); !ok {
		t.Error("cannot read emitted rules")
	}
}

func TestLegacyGeneratedRulesRemainEditable(t *testing.T) {
	const legacy = `# Rules, composed on the Rules page. They run in this order; a rule that stops ends delivery here.
require ["fileinto", "imap4flags"];

# rule: filed
if allof(header :contains "subject" "notice") {
    fileinto "Archive";
    addflag "\\Seen";
    addflag "\\Flagged";
}
`
	rules, ok := readRules(legacy)
	if !ok || len(rules) != 1 || !rules[0].MarkRead || !rules[0].Star || rules[0].Folder != "Archive" {
		t.Fatalf("old generated rule not recognized: %+v, %v", rules, ok)
	}
	if _, ok := readRules(legacy + "keep;\n"); ok {
		t.Error("customized legacy script was recognized as generated")
	}
}
