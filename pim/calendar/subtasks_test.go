package calendar

import (
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

func TestParentUIDUsesTheICalendarRelationship(t *testing.T) {
	for _, tc := range []struct{ relation, want string }{
		{"RELATED-TO:parent", "parent"},
		{"RELATED-TO;RELTYPE=PARENT:parent", "parent"},
		{"RELATED-TO;RELTYPE=SIBLING:sibling", ""},
		{"RELATED-TO;RELTYPE=CHILD:child", ""},
	} {
		cal, err := ical.NewDecoder(strings.NewReader("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VTODO\r\nUID:child\r\n" + tc.relation + "\r\nEND:VTODO\r\nEND:VCALENDAR\r\n")).Decode()
		if err != nil {
			t.Fatal(err)
		}
		if got := ParentUID(FirstTodo(cal)); got != tc.want {
			t.Errorf("%s = %q", tc.relation, got)
		}
	}
	todo := ical.NewComponent(ical.CompToDo)
	SetParent(todo, "parent,escaped")
	if got := ParentUID(todo); got != "parent,escaped" {
		t.Fatalf("parent round trip = %q", got)
	}
	SetParent(todo, "other")
	if got := len(todo.Props["RELATED-TO"]); got != 1 || ParentUID(todo) != "other" {
		t.Fatalf("replace kept %d relations, parent %q", got, ParentUID(todo))
	}
	SetParent(todo, "")
	if ParentUID(todo) != "" {
		t.Fatal("empty uid kept the parent")
	}
}
