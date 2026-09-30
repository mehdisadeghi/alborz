package mail

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/imapsearch"
	"git.mehdix.org/alborz/search"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// mergedWindow is a merged folder's rows up to the page asked, merged as its
// page is; found is false where every account answered without the folder.
// down are the accounts whose servers did not answer.
func (p *Mailboxes) mergedWindow(ctx context.Context, logins []*alborz.Login, role string, ask ListAsk, settings *Settings, cacheable bool) (merged *listingEntry, found bool, down []string, err error) {
	spec := ask.spec("")
	key := listingView("#"+role, spec.query, spec.view, spec.sortKey, spec.sortDir)
	class, bound := listingBudget(spec)
	// Every account that answered lacking the folder is a folder that is not there.
	var holding, silent atomic.Int32
	merged, down, err = p.gather(ctx, logins, spec, func(s *alborz.Login) (e *listingEntry, err error) {
		defer func() {
			if e != nil {
				holding.Add(1)
			}
			if err != nil {
				silent.Add(1)
			}
		}()
		user := s.Username()
		size := ask.held(p.listings.heldSize(user, key))
		fields := mergeFetchOptions
		if size == ask.Page.Size {
			fields = listFetchOptions
		}
		folder := func(c *imapclient.Client) (string, error) { return p.resolveRole(c, user, role) }
		fetch := func(c *imapclient.Client) (*listingEntry, error) {
			name, err := folder(c)
			if err != nil || name == "" {
				return nil, err
			}
			return p.fetchUnifiedAccount(c, user, name, spec, settings, size, fields, cacheable)
		}
		if cacheable {
			return p.cachedListing(ctx, s, key, size, spec, folder, fetch)
		}
		alborz.CacheTiming(ctx, "listing", false)
		return readOn(s, class, bound, fetch)(ctx)
	})
	if err != nil {
		return nil, false, nil, err
	}
	return merged, holding.Load() > 0 || silent.Load() > 0, down, nil
}

// Rounded up to a power of two so a walk through n pages refetches log n times (ADR 46).
func (ask ListAsk) held(have int) int {
	pages := 1
	for pages < ask.Page.Number+1 {
		pages *= 2
	}
	return max(pages*ask.Page.Size, have)
}

// A row that could not be fetched is left off rather than shown without its marks.
func (p *Mailboxes) wholePage(ctx context.Context, logins []*alborz.Login, role, key string, page []IMAPMessage, class alborz.IMAPClass, bound time.Duration) ([]IMAPMessage, []string, error) {
	missing := map[string][]imap.UID{}
	for _, m := range page {
		if m.BodyStructure == nil {
			missing[m.Account] = append(missing[m.Account], m.UID)
		}
	}
	if len(missing) == 0 {
		return page, nil, nil
	}
	fetched, down, err := p.gather(ctx, logins, listingSpec{}, func(s *alborz.Login) (*listingEntry, error) {
		user, uids := s.Username(), missing[s.Username()]
		if len(uids) == 0 {
			return nil, nil
		}
		return readOn(s, class, bound, func(c *imapclient.Client) (*listingEntry, error) {
			name, err := p.resolveRole(c, user, role)
			if err != nil || name == "" {
				return nil, err
			}
			rows, err := wholeRows(c, name, uids)
			if err != nil {
				return nil, err
			}
			for i := range rows {
				rows[i].Account = user
			}
			p.listings.fill(user, key, rows)
			return &listingEntry{msgs: rows}, nil
		})(ctx)
	})
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		account string
		uid     imap.UID
	}
	whole := make(map[row]IMAPMessage, len(fetched.msgs))
	for _, m := range fetched.msgs {
		whole[row{m.Account, m.UID}] = m
	}
	rows := make([]IMAPMessage, 0, len(page))
	for _, m := range page {
		if m.BodyStructure == nil {
			var ok bool
			if m, ok = whole[row{m.Account, m.UID}]; !ok {
				continue
			}
		}
		rows = append(rows, m)
	}
	return rows, down, nil
}

// The page is cut from a merged window, which holds every row up to it.
func cutPage(merged *listingEntry, page alborz.Page) (alborz.Page, []IMAPMessage) {
	page = page.Within(merged.total)
	from, to := page.Rows(len(merged.msgs))
	return page, merged.msgs[from:to]
}

// A silent server costs its account's rows, not the page, and is named in down; only when nobody answers is it an error.
func (p *Mailboxes) gather(ctx context.Context, logins []*alborz.Login, spec listingSpec, read func(*alborz.Login) (*listingEntry, error)) (*listingEntry, []string, error) {
	var wg sync.WaitGroup
	// One span for the whole fan-out: the accounts are queried concurrently.
	imapStart := time.Now()
	errs := make([]error, len(logins))
	asked := func(i int, s *alborz.Login) *listingEntry {
		e, err := read(s)
		if errs[i] = err; err != nil || e == nil {
			return nil
		}
		return e
	}
	entries := make([]*listingEntry, len(logins))
	for i, s := range logins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entries[i] = asked(i, s)
		}()
	}
	wg.Wait()
	alborz.AddTiming(ctx, "imap", imapStart)
	// Absorbed in the accounts' order, so the stable sort keeps equal rows in place across requests.
	merged := &listingEntry{sortSupported: true}
	for _, e := range entries {
		if e != nil {
			merged.absorb(e)
		}
	}
	var down []string
	for i, err := range errs {
		var upstream alborz.UpstreamError
		switch {
		case err == nil:
		case errors.As(err, &upstream):
			p.server.Logger().Printf("merged view without %q: %v", logins[i].Username(), err)
			down = append(down, logins[i].Username())
		default:
			return nil, nil, err
		}
	}
	if len(down) == len(errs) {
		return nil, nil, errs[0]
	}
	slices.SortStableFunc(merged.msgs, unifiedLess(spec.sortKey, spec.reverse()))
	return merged, down, nil
}

func unifiedLess(sortKey string, reverse bool) func(a, b IMAPMessage) int {
	byDate := func(a, b IMAPMessage) int {
		return b.Date().Compare(a.Date())
	}
	var cmp func(a, b IMAPMessage) int
	switch sortKey {
	case "from":
		cmp = func(a, b IMAPMessage) int {
			return strings.Compare(strings.ToLower(envelopeName(a.Envelope.From)), strings.ToLower(envelopeName(b.Envelope.From)))
		}
	case "subject":
		cmp = func(a, b IMAPMessage) int {
			return strings.Compare(strings.ToLower(a.Envelope.Subject), strings.ToLower(b.Envelope.Subject))
		}
	case "size":
		cmp = func(a, b IMAPMessage) int {
			switch {
			case a.RFC822Size < b.RFC822Size:
				return -1
			case a.RFC822Size > b.RFC822Size:
				return 1
			}
			return 0
		}
	case "account":
		cmp = func(a, b IMAPMessage) int {
			return strings.Compare(strings.ToLower(a.Account), strings.ToLower(b.Account))
		}
	case "starred":
		cmp = func(a, b IMAPMessage) int {
			af, bf := a.HasFlag(imap.FlagFlagged), b.HasFlag(imap.FlagFlagged)
			if af == bf {
				return 0
			}
			if af {
				return -1
			}
			return 1
		}
	default:
		if reverse {
			return func(a, b IMAPMessage) int { return byDate(a, b) }
		}
		return func(a, b IMAPMessage) int { return -byDate(a, b) }
	}
	return func(a, b IMAPMessage) int {
		c := cmp(a, b)
		if reverse {
			c = -c
		}
		if c != 0 {
			return c
		}
		return byDate(a, b)
	}
}

func (p *Mailboxes) fetchUnifiedAccount(c *imapclient.Client, user, folder string, spec listingSpec, settings *Settings, window int, fields rowFields, withSnap bool) (*listingEntry, error) {
	var snapCmd *imapclient.StatusCommand
	if withSnap {
		snapCmd = c.Status(folder, listingStatusOptions(c))
	}
	e, err := p.fetchRows(c, user, folder, spec, settings, alborz.Page{Size: window}, fields)
	if err != nil {
		return nil, err
	}
	for j := range e.msgs {
		e.msgs[j].Account = user
	}
	if snapCmd != nil {
		if st, err := snapCmd.Wait(); err == nil {
			e.snap = st
		}
	}
	return e, nil
}

// A search leaves junk and trash out until it is asked for them.
func Everywhere(query string) string {
	if query == "" {
		return ""
	}
	q := search.Parse(query)
	if _, everywhere := imapsearch.Scope(q); everywhere {
		return ""
	}
	return strings.TrimSpace(q.Without(imapsearch.In) + " " + imapsearch.In + ":" + imapsearch.Everywhere)
}

// ListAsk is the page of a list an address asks for, narrowed and ordered as it says.
type ListAsk struct {
	Page                   alborz.Page
	Query, View, Sort, Dir string
	// Thread is one conversation of a folder, asked for alone; zero for the whole folder.
	Thread imap.UID
}

func (a ListAsk) spec(folder string) listingSpec {
	return listingSpec{mbox: folder, query: a.Query, view: a.View, sortKey: a.Sort, sortDir: a.Dir, thread: a.Thread}
}

// Descending says the list runs from its last row: as asked, else as its order runs by default.
func (a ListAsk) Descending() bool { return a.spec("").reverse() }

// KnownSort says whether a list can be ordered by key.
func KnownSort(key string) bool {
	_, ok := sortKeys[key]
	return ok
}

// Listing is a page of a list as its page shows it.
type Listing struct {
	Rows  []IMAPMessage
	Page  alborz.Page
	Total int
	// HeadersOnly says a search's bare terms reached only the headers.
	HeadersOnly     bool
	SortSupported   bool
	ThreadAlgorithm imap.ThreadAlgorithm
	// Rail is the account's folders beside a folder's list; empty beside one spanning accounts.
	Rail Rail
}

// Folder is one page of a folder's list; cached lets it answer from what is held, and read ahead the
// bodies of its rows and the next page.
func (p *Mailboxes) Folder(ctx context.Context, login *alborz.Login, settings *Settings, folder string, ask ListAsk, cached, preferHTML bool) (*Listing, error) {
	spec := ask.spec(folder)
	key := listingView(folder, spec.query, spec.view, spec.sortKey, spec.sortDir)
	if spec.thread != 0 {
		key = fmt.Sprintf("%s%sthread=%d", key, listingSep, spec.thread)
	}
	key = listingPage(key, ask.Page.Number, ask.Page.Size)
	fetch := func(c *imapclient.Client) (*listingEntry, error) {
		return p.fetchListing(c, login.Username(), spec, settings, ask.Page)
	}
	var e *listingEntry
	var err error
	if cached {
		e, err = p.cachedListing(ctx, login, key, ask.Page.Size, spec,
			func(*imapclient.Client) (string, error) { return folder, nil }, fetch)
	} else {
		e, err = p.loadListing(ctx, login, key, ask.Page.Size, spec, fetch)
	}
	if err != nil {
		return nil, err
	}
	sb, msgs := p.railFor(login, folder, e.sb), e.msgs
	page := alborz.Page{Number: e.page, Size: e.perPage}
	// Bodies are fetched behind the page, so the next click asks the server nothing.
	if cached {
		p.prefetchBodies(login, preferHTML, folder, e.validity(), e.msgs)
		if spec.sortKey != SortThread && spec.thread == 0 && page.Window() < e.total {
			p.prefetchPage(login, settings, spec, alborz.Page{Number: page.Number + 1, Size: page.Size})
		}
	}
	trust := p.newDeliveryTrust(login, settings)
	for i := range msgs {
		msgs[i].Alias = trust.alias(&msgs[i])
	}
	return &Listing{Rows: msgs, Page: page, Total: e.total, HeadersOnly: e.headersOnly, SortSupported: e.sortSupported,
		ThreadAlgorithm: e.threadAlgorithm, Rail: sb}, nil
}

// Merged is one page of a merged folder: a role's folder in every account, or a folder by name in the
// accounts that hold it. found is false where every account answered without it; down names the
// accounts whose servers did not answer.
func (p *Mailboxes) Merged(ctx context.Context, logins []*alborz.Login, settings *Settings, role string, ask ListAsk, cached bool) (l *Listing, found bool, down []string, err error) {
	window, found, down, err := p.mergedWindow(ctx, logins, role, ask, settings, cached)
	if err != nil || !found {
		return nil, found, down, err
	}
	spec := ask.spec("")
	class, bound := listingBudget(spec)
	page, cut := cutPage(window, ask.Page)
	rows, more, err := p.wholePage(ctx, logins, role, listingView("#"+role, spec.query, spec.view, spec.sortKey, spec.sortDir), cut, class, bound)
	if err != nil {
		return nil, true, down, err
	}
	return &Listing{Rows: rows, Page: page, Total: window.total, HeadersOnly: window.headersOnly,
		SortSupported: window.sortSupported}, true, append(down, more...), nil
}

// Search is one page of a search's rows, from every folder it spans; down names the accounts whose
// servers did not answer.
func (p *Mailboxes) Search(ctx context.Context, logins []*alborz.Login, settings *Settings, ask ListAsk) (*Listing, []string, error) {
	users := make([]string, len(logins))
	for i, s := range logins {
		users[i] = s.Username()
	}
	ctx, done := p.listings.supersede(ctx, users)
	defer done()
	window, down, err := p.searchWindow(ctx, logins, ask, settings)
	if errors.Is(context.Cause(ctx), alborz.ErrSuperseded) {
		return nil, nil, alborz.ErrSuperseded
	}
	if err != nil {
		return nil, nil, err
	}
	page, rows := cutPage(window, ask.Page)
	return &Listing{Rows: rows, Page: page, Total: window.total, HeadersOnly: window.headersOnly,
		SortSupported: window.sortSupported}, down, nil
}

// An order the server cannot sort by is no order at all here.
func (l ListAsk) order() (sortKey string, reverse bool) {
	sortKey = l.Sort
	if _, ok := sortKeys[sortKey]; !ok || sortKey == SortThread {
		return "", true
	}
	reverse = sortKeys[sortKey].descends
	if l.Dir != "" {
		reverse = l.Dir == "desc"
	}
	return sortKey, reverse
}

func (p *Mailboxes) fetchListing(c *imapclient.Client, user string, spec listingSpec, settings *Settings, page alborz.Page) (*listingEntry, error) {
	load, err := startSidebar(c, spec.mbox, spec.mbox, settings.Subscriptions)
	if err != nil {
		return nil, err
	}
	e, err := p.fetchRows(c, user, spec.mbox, spec, settings, page, listFetchOptions)
	if err != nil {
		return nil, err
	}
	if e.sb, err = load.finish(); err != nil {
		return nil, err
	}
	e.snap = e.sb.Active.StatusData
	return e, nil
}

func (p *Mailboxes) fetchRows(c *imapclient.Client, user, folder string, spec listingSpec, settings *Settings, page alborz.Page, fields rowFields) (*listingEntry, error) {
	e := &listingEntry{perPage: page.Size, sortSupported: c.Caps().Has(imap.CapSort), threadAlgorithm: ThreadAlgorithm(c)}
	// Each window is cut under the requested order, so the merge sees the true candidates.
	sortKey, reverse := spec.sortKey, spec.reverse()
	if sortKey == "account" {
		sortKey, reverse = "", true
	}
	var err error
	switch {
	case spec.thread != 0 && e.threadAlgorithm != "":
		e.msgs, err = oneThread(c, folder, e.threadAlgorithm, spec.thread)
		e.total = len(e.msgs)
	case spec.sortKey == SortThread && e.threadAlgorithm != "":
		e.headersOnly = spec.query != "" && !SearchesIndex(c, settings)
		if err = ensureMailboxSelected(c, folder); err != nil {
			return nil, err
		}
		criteria := listCriteria(c, settings, spec.query, spec.view)
		var o messageOrder
		o, err = p.listings.order(user, orderView(folder, spec.query, spec.view, SortThread, true), func() (messageOrder, error) {
			return threadOrder(c, e.threadAlgorithm, criteria)
		})
		if err == nil {
			e.total = len(o.starts)
			e.msgs, err = threadPage(c, folder, o, page)
		}
	case spec.query != "" || spec.view != "" || spec.sortKey != "account" && (spec.sortKey != "" || spec.sortDir != ""):
		e.headersOnly = spec.query != "" && !SearchesIndex(c, settings)
		if err = ensureMailboxSelected(c, folder); err != nil {
			return nil, err
		}
		criteria := listCriteria(c, settings, spec.query, spec.view)
		var o messageOrder
		o, err = p.listings.order(user, orderView(folder, spec.query, spec.view, sortKey, reverse), func() (messageOrder, error) {
			uids, err := viewOrder(c, search.Parse(spec.query), criteria, sortKey, reverse)
			return messageOrder{uids: uids}, err
		})
		if err == nil {
			e.total = len(o.uids)
			e.msgs, err = orderedPage(c, folder, o.uids, page, fields)
		}
	default:
		e.msgs, e.total, err = listMessages(c, folder, page, fields)
	}
	if err != nil {
		return nil, err
	}
	e.page = page.Within(e.total).Number
	return e, nil
}

// TextQuery is the query widened to the whole message, when the search reached headers only.
func TextQuery(query string, headersOnly bool) string {
	if !headersOnly {
		return ""
	}
	return imapsearch.Widened(search.Parse(query))
}

// Outgoing says a folder holds what the reader wrote, so its rows name recipients.
func Outgoing(mailboxes []MailboxInfo, name string) bool {
	role := folderRole(mailboxes, name)
	return role == "sent" || role == "drafts"
}

func folderRole(mailboxes []MailboxInfo, name string) string {
	for i := range mailboxes {
		if mailboxes[i].Name() == name {
			return mailboxes[i].Role()
		}
	}
	return ""
}

func searchFolders(mailboxes []MailboxInfo, q search.Query) []string {
	named, everywhere := imapsearch.Scope(q)
	excluded := imapsearch.Excluded(q)
	var out []string
	for i := range mailboxes {
		mbox := &mailboxes[i]
		if mbox.IsInternal() || slices.ContainsFunc(excluded, func(name string) bool { return folderNamed(mbox, name) }) {
			continue
		}
		role := mbox.Role()
		switch {
		case named != "":
			if !folderNamed(mbox, named) {
				continue
			}
		case !everywhere && (role == "junk" || role == "trash"):
			continue
		}
		out = append(out, mbox.Name())
	}
	return out
}

func folderNamed(mbox *MailboxInfo, named string) bool {
	role := mbox.Role()
	last := mbox.Name()[strings.LastIndex(mbox.Name(), string(mbox.Delim))+1:]
	return strings.EqualFold(named, role) || strings.EqualFold(named, mbox.Name()) || strings.EqualFold(named, last) ||
		(role == "junk" && strings.EqualFold(named, "spam"))
}

// searchWindow is a search's rows up to the page asked, from every folder it spans, merged as its page is.
func (p *Mailboxes) searchWindow(ctx context.Context, logins []*alborz.Login, ask ListAsk, settings *Settings) (*listingEntry, []string, error) {
	spec := ask.spec("")
	q := search.Parse(spec.query)
	class, bound := listingBudget(spec)
	return p.gather(ctx, logins, spec, func(s *alborz.Login) (*listingEntry, error) {
		sb, err := p.Rail(s)
		if err != nil {
			return nil, err
		}
		// One turn per folder, so the account's other pages are served between them.
		found := &listingEntry{sortSupported: true}
		for _, folder := range searchFolders(sb.Mailboxes, q) {
			e, err := readOn(s, class, bound, func(c *imapclient.Client) (*listingEntry, error) {
				return p.fetchUnifiedAccount(c, s.Username(), folder, spec, settings, ask.Page.Window(), listFetchOptions, false)
			})(ctx)
			if err != nil {
				return nil, err
			}
			found.absorb(e)
		}
		return found, nil
	})
}

// heldFields are the headers a held row answers a key from; a bare term is asked of them all, as the
// server asks a search that reaches only the headers.
var heldFields = map[string]func(env *imap.Envelope) string{
	"from":    func(env *imap.Envelope) string { return addressesText(env.From) },
	"to":      func(env *imap.Envelope) string { return addressesText(env.To) },
	"cc":      func(env *imap.Envelope) string { return addressesText(env.Cc) },
	"subject": func(env *imap.Envelope) string { return env.Subject },
}

func addressesText(list []imap.Address) string {
	var b strings.Builder
	for _, a := range list {
		b.WriteString(a.Name + " <" + a.Addr() + ">\n")
	}
	return b.String()
}

// Held is up to limit rows, newest first, of the accounts' mail alborz holds from the lists it has read,
// that the query finds in their headers. Junk and trash are left out, as a search leaves them. ok is
// false when the query asks what a row's headers cannot tell.
func (p *Mailboxes) Held(logins []*alborz.Login, q search.Query, limit int) (rows []IMAPMessage, ok bool) {
	for _, term := range q {
		if _, known := heldFields[term.Key]; term.Key != "" && !known {
			return nil, false
		}
	}
	users := make(map[string]bool, len(logins))
	for _, s := range logins {
		users[s.Username()] = true
	}
	for _, m := range p.listings.held(users) {
		if m.Envelope != nil && heldMatches(q, m.Envelope) {
			rows = append(rows, m)
		}
	}
	slices.SortStableFunc(rows, unifiedLess("", true))
	return rows[:min(limit, len(rows))], true
}

func heldMatches(q search.Query, env *imap.Envelope) bool {
	for _, term := range q {
		var text string
		if read := heldFields[term.Key]; read != nil {
			text = read(env)
		} else {
			for _, key := range []string{"from", "to", "cc", "subject"} {
				text += heldFields[key](env) + "\n"
			}
		}
		if strings.Contains(strings.ToLower(text), strings.ToLower(term.Value)) == term.Not {
			return false
		}
	}
	return true
}
