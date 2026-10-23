package alborz

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGestureDefaultsAndExplicitNone(t *testing.T) {
	want := Gestures{
		Mail: SwipeActions{"delete", "archive"}, Tasks: SwipeActions{"delete", "complete"},
		Contacts: SwipeActions{"delete", "star"}, Journal: SwipeActions{"delete", "star"},
		Events: SwipeActions{"delete", "none"},
	}
	if got := (Gestures{}).Resolved(); got != want {
		t.Fatalf("default swipes = %#v, want %#v", got, want)
	}
	g := Gestures{Mail: SwipeActions{Left: "none", Right: "junk"}, Events: SwipeActions{Right: "star"}}
	if !g.Valid() || g.Resolved().Mail != g.Mail || g.Resolved().Events.Right != "star" {
		t.Fatalf("explicit choices were not retained: %#v", g.Resolved())
	}
	g.Mail.Right = "complete"
	if g.Valid() || g.Resolved().Mail.Right != "archive" {
		t.Fatalf("unsupported mail action was accepted: %#v", g.Resolved())
	}
}

func TestGestureJSONUsesLowercaseSectionsAndDirections(t *testing.T) {
	b, err := json.Marshal(Gestures{}.Resolved())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{
		"mail": {"left": "delete", "right": "archive"}, "tasks": {"left": "delete", "right": "complete"},
		"contacts": {"left": "delete", "right": "star"}, "journal": {"left": "delete", "right": "star"},
		"events": {"left": "delete", "right": "none"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gesture JSON = %s", b)
	}
}

func TestGestureActionsStayWithinTheirSections(t *testing.T) {
	for _, test := range []struct {
		section, action string
		valid           bool
	}{
		{"mail", "junk", true}, {"mail", "star", false},
		{"tasks", "complete", true}, {"tasks", "archive", false},
		{"contacts", "star", true}, {"contacts", "complete", false},
		{"journal", "none", true}, {"journal", "junk", false},
		{"events", "star", true}, {"events", "complete", false},
	} {
		t.Run(test.section+"/"+test.action, func(t *testing.T) {
			b, err := json.Marshal(map[string]map[string]string{test.section: {"right": test.action}})
			if err != nil {
				t.Fatal(err)
			}
			var g Gestures
			if err := json.Unmarshal(b, &g); err != nil {
				t.Fatal(err)
			}
			if got := g.Valid(); got != test.valid {
				t.Fatalf("%s %s valid = %t, want %t", test.section, test.action, got, test.valid)
			}
		})
	}
}
