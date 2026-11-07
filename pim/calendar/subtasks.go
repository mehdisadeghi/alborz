package calendar

import (
	"slices"
	"strings"

	"github.com/emersion/go-ical"
)

// isParent follows RFC 5545 3.8.4.5: subtasks link to a parent VTODO in the
// same collection via RELATED-TO;RELTYPE=PARENT (an omitted RELTYPE defaults
// to PARENT). Completing a parent or subtask never cascades to relatives.
func isParent(relation ical.Prop) bool {
	kind := strings.ToUpper(relation.Params.Get("RELTYPE"))
	return kind == "" || kind == "PARENT"
}

func ParentUID(todo *ical.Component) string {
	for _, relation := range todo.Props["RELATED-TO"] {
		if isParent(relation) {
			uid, _ := relation.Text()
			return uid
		}
	}
	return ""
}

// SetParent replaces the parent, or drops it for an empty uid; sibling/child relationships survive.
func SetParent(todo *ical.Component, uid string) {
	todo.Props["RELATED-TO"] = slices.DeleteFunc(todo.Props["RELATED-TO"], isParent)
	if uid == "" {
		return
	}
	prop := ical.NewProp("RELATED-TO")
	prop.Params.Set("RELTYPE", "PARENT")
	prop.SetText(uid)
	todo.Props.Add(prop)
}
