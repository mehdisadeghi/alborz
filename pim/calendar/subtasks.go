package calendar

import (
	"slices"
	"strings"

	"github.com/emersion/go-ical"
)

// isParent follows RFC 5545: an omitted RELTYPE means PARENT.
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
