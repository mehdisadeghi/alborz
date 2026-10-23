package calendar

import (
	"strings"

	"github.com/emersion/go-ical"
)

// ParentUID follows RFC 5545: an omitted RELTYPE means PARENT.
func ParentUID(todo *ical.Component) string {
	for _, relation := range todo.Props["RELATED-TO"] {
		kind := strings.ToUpper(relation.Params.Get("RELTYPE"))
		if kind == "" || kind == "PARENT" {
			uid, _ := relation.Text()
			return uid
		}
	}
	return ""
}

// SetParent is used on a new task; unrelated sibling/child relationships survive.
func SetParent(todo *ical.Component, uid string) {
	prop := ical.NewProp("RELATED-TO")
	prop.Params.Set("RELTYPE", "PARENT")
	prop.SetText(uid)
	todo.Props.Add(prop)
}
