package alborz

// Reading is what a person wants from the pages, as against what an
// account is: how many messages fit, which clock and calendar the dates
// are in, whether HTML is preferred. Nobody can say which account these
// belong to, which is why a merged page used to take them from
// whichever address sorted first. They belong to the visit.
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

// Reading is what this browser reads by. It is the visit's own, seeded
// once from the first account that brought some. What was never chosen
// comes back as the default rather than as a zero: a form renders what
// is in force, and a page size of none is not in force anywhere.
func (ctx *Context) Reading() Reading {
	v := ctx.lookupVisit()
	if v == nil {
		return Reading{MessagesPerPage: DefaultMessagesPerPage}
	}
	r := v.reading()
	if r.MessagesPerPage <= 0 {
		r.MessagesPerPage = DefaultMessagesPerPage
	}
	return r
}

// SetReading records the choice and keeps it on the anchor account, so
// the next browser signed into that account starts where this one left
// off. An unanchored visit keeps it to itself.
func (ctx *Context) SetReading(r Reading) error {
	v := ctx.Visit()
	v.setReading(r)
	if err := ctx.Server.Visits.Save(v); err != nil {
		return err
	}
	anchor := v.Anchor()
	if anchor == "" {
		return nil
	}
	return ctx.Server.Visits.SaveReading(anchor, r)
}

// SetAnchor names the account this browser's reading settings are kept
// on, or none. Naming one writes what the browser is reading by now:
// the anchor is written to, never read back, so nothing changes under
// the reader at the moment they choose.
func (ctx *Context) SetAnchor(account string) error {
	v := ctx.Visit()
	v.setAnchor(account)
	if err := ctx.Server.Visits.Save(v); err != nil {
		return err
	}
	if account == "" {
		return nil
	}
	return ctx.Server.Visits.SaveReading(account, v.reading())
}

// adoptReading seeds a browser that has none of its own from an account
// that brought some, and reports whether it did. A reader who has
// already chosen in this browser is never overruled.
func (ctx *Context) adoptReading(account string) bool {
	v := ctx.lookupVisit()
	if v == nil || v.hasReading() {
		return false
	}
	r, ok := ctx.Server.Visits.LoadReading(account)
	if !ok {
		// Nothing stored: this account becomes the anchor anyway, so
		// what the reader chooses next is kept somewhere.
		v.setAnchor(account)
		return false
	}
	v.setReading(*r)
	v.setAnchor(account)
	ctx.Server.Visits.Save(v)
	return true
}
