package alborz

import "slices"

// SwipeActions names physical left and right row actions; empty follows the default.
type SwipeActions struct {
	Left  string `json:"left"`
	Right string `json:"right"`
}

// Gestures is the account-synced row mapping. Browser-local switches are separate.
type Gestures struct {
	Mail     SwipeActions `json:"mail"`
	Tasks    SwipeActions `json:"tasks"`
	Contacts SwipeActions `json:"contacts"`
	Journal  SwipeActions `json:"journal"`
	Events   SwipeActions `json:"events"`
}

// GestureActions lists the actions supported by a section, including explicit none.
func GestureActions(section string) []string {
	switch section {
	case "mail":
		return []string{"none", "delete", "archive", "junk"}
	case "tasks":
		return []string{"none", "delete", "complete", "star"}
	case "contacts", "journal", "events":
		return []string{"none", "delete", "star"}
	}
	return nil
}

// Valid reports whether every explicit action belongs to its section.
func (g Gestures) Valid() bool {
	for section, pair := range map[string]SwipeActions{
		"mail": g.Mail, "tasks": g.Tasks, "contacts": g.Contacts, "journal": g.Journal, "events": g.Events,
	} {
		for _, action := range []string{pair.Left, pair.Right} {
			if action != "" && !slices.Contains(GestureActions(section), action) {
				return false
			}
		}
	}
	return true
}

// Resolved replaces empty or unrecognised stored actions with safe section defaults.
func (g Gestures) Resolved() Gestures {
	resolve := func(section string, pair SwipeActions, right string) SwipeActions {
		if !slices.Contains(GestureActions(section), pair.Left) {
			pair.Left = "delete"
		}
		if !slices.Contains(GestureActions(section), pair.Right) {
			pair.Right = right
		}
		return pair
	}
	g.Mail = resolve("mail", g.Mail, "archive")
	g.Tasks = resolve("tasks", g.Tasks, "complete")
	g.Contacts = resolve("contacts", g.Contacts, "star")
	g.Journal = resolve("journal", g.Journal, "star")
	g.Events = resolve("events", g.Events, "none")
	return g
}
