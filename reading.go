package alborz

// Reading is what a person wants from the pages, as against what an
// account is: how many messages fit, which clock and calendar the dates
// are in, whether HTML is preferred. Nobody can say which account these
// belong to, which is why a merged page used to take them from
// whichever address sorted first. They belong to the session.
//
// They are kept on one account, the anchor, so that signing in on
// another machine brings them back. That account is the only writer,
// so there is nothing to merge and nothing to go stale.
type Reading struct {
	MessagesPerPage int
	Timezone        string
	FirstDayOfWeek  int // 0 = Sunday, 1 = Monday (default)
	PreferHTML      bool
	// Primary is the calendar the pages count in, Secondary the one
	// glossed beside it; empty means Gregorian and none.
	Primary   string
	Secondary string
}

// DefaultMessagesPerPage is the page size a reader who has never chosen
// one reads by. Zero is not a page size, so it is what an unset value
// means rather than a value in its own right.
const DefaultMessagesPerPage = 50

// Reading is what this browser reads by. It is the session's own, seeded
// once from the first account that brought some. What was never chosen
// comes back as the default rather than as a zero: a form renders what
// is in force, and a page size of none is not in force anywhere.
func (ctx *Context) Reading() Reading {
	s := ctx.lookupSession()
	if s == nil {
		return Reading{MessagesPerPage: DefaultMessagesPerPage}
	}
	r := s.reading()
	if r.MessagesPerPage <= 0 {
		r.MessagesPerPage = DefaultMessagesPerPage
	}
	return r
}

// SetReading records the choice and keeps it on the anchor account, so
// the next browser signed into that account starts where this one left
// off. An unanchored session keeps it to itself.
func (ctx *Context) SetReading(r Reading) error {
	s := ctx.Session()
	s.setReading(r)
	if err := ctx.Server.Sessions.Save(s); err != nil {
		return err
	}
	anchor := s.Anchor()
	if anchor == "" {
		return nil
	}
	return ctx.Server.Sessions.SaveReading(anchor, r)
}

// SetAnchor names the account this browser's reading settings are kept
// on, or none. Naming one writes what the browser is reading by now:
// the anchor is written to, never read back, so nothing changes under
// the reader at the moment they choose.
func (ctx *Context) SetAnchor(account string) error {
	s := ctx.Session()
	s.setAnchor(account)
	if err := ctx.Server.Sessions.Save(s); err != nil {
		return err
	}
	if account == "" {
		return nil
	}
	return ctx.Server.Sessions.SaveReading(account, s.reading())
}

// adoptReading seeds a browser that has none of its own from an account
// that brought some, and reports whether it did. A reader who has
// already chosen in this browser is never overruled.
func (ctx *Context) adoptReading(account string) bool {
	s := ctx.lookupSession()
	if s == nil || s.hasReading() {
		return false
	}
	r, ok := ctx.Server.Sessions.LoadReading(account)
	if !ok {
		// Nothing stored: this account becomes the anchor anyway, so
		// what the reader chooses next is kept somewhere.
		s.setAnchor(account)
		return false
	}
	s.setReading(*r)
	s.setAnchor(account)
	ctx.Server.Sessions.Save(s)
	return true
}
