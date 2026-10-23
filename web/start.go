package web

// DefaultPath is the reader's chosen section, preserving an explicit account.
func (ctx *Context) DefaultPath() string {
	switch ctx.Profile().Reading.StartPage {
	case "calendar":
		return ctx.URL("calendar.month")
	case "contacts":
		return ctx.URL("contacts.list")
	case "tasks":
		return ctx.URL("calendar.tasks")
	case "journal":
		return ctx.URL("calendar.journal")
	default:
		return ctx.URL("mail.mailbox", "INBOX")
	}
}
