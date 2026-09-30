package mail

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/search"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// mergedWindow is a merged folder's rows up to the page asked, merged as its
// page is; found is false where every account answered without the folder.
// down are the accounts whose servers did not answer.
func (p *plugin) mergedWindow(ctx context.Context, logins []*alborz.Login, role string, ask listAsk, settings *Settings, cacheable bool) (merged *listingEntry, found bool, down []string, err error) {
	spec := ask.spec
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
		if size == ask.page.Size {
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
func (ask listAsk) held(have int) int {
	pages := 1
	for pages < ask.page.Number+1 {
		pages *= 2
	}
	return max(pages*ask.page.Size, have)
}

// A row that could not be fetched is left off rather than shown without its marks.
func (p *plugin) wholePage(ctx context.Context, logins []*alborz.Login, role, key string, page []IMAPMessage, class alborz.IMAPClass, bound time.Duration) ([]IMAPMessage, []string, error) {
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
func (p *plugin) gather(ctx context.Context, logins []*alborz.Login, spec listingSpec, read func(*alborz.Login) (*listingEntry, error)) (*listingEntry, []string, error) {
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

func (p *plugin) fetchUnifiedAccount(c *imapclient.Client, user, folder string, spec listingSpec, settings *Settings, window int, fields rowFields, withSnap bool) (*listingEntry, error) {
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
func everywhereQuery(query string) string {
	if query == "" {
		return ""
	}
	q := search.Parse(query)
	if _, everywhere := q.Scope(); everywhere {
		return ""
	}
	return strings.TrimSpace(q.Without(search.In) + " " + search.In + ":" + search.Everywhere)
}

type listAsk struct {
	page alborz.Page
	spec listingSpec
}

// folderList is the folder's list a message was opened from, as the message's address names it.
type folderList struct{ query, view, sort, dir string }

// An order the server cannot sort by is no order at all here.
func (l folderList) order() (sortKey string, reverse bool) {
	sortKey = l.sort
	if _, ok := sortKeys[sortKey]; !ok || sortKey == threadSort {
		return "", true
	}
	reverse = sortKeys[sortKey].descends
	if l.dir != "" {
		reverse = l.dir == "desc"
	}
	return sortKey, reverse
}

func (p *plugin) fetchListing(c *imapclient.Client, user string, spec listingSpec, settings *Settings, page alborz.Page) (*listingEntry, error) {
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
	e.snap = e.sb.active.StatusData
	return e, nil
}

func (p *plugin) fetchRows(c *imapclient.Client, user, folder string, spec listingSpec, settings *Settings, page alborz.Page, fields rowFields) (*listingEntry, error) {
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
	case spec.sortKey == threadSort && e.threadAlgorithm != "":
		e.headersOnly = spec.query != "" && !SearchesIndex(c, settings)
		if err = ensureMailboxSelected(c, folder); err != nil {
			return nil, err
		}
		criteria := listCriteria(c, settings, spec.query, spec.view)
		var o messageOrder
		o, err = p.listings.order(user, orderView(folder, spec.query, spec.view, threadSort, true), func() (messageOrder, error) {
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

func textQueryOffered(query string, headersOnly bool) string {
	if !headersOnly {
		return ""
	}
	return search.Parse(query).Widened()
}

func outgoingFolder(mailboxes []MailboxInfo, name string) bool {
	role := folderRole(mailboxes, name)
	return role == "sent" || role == "drafts"
}

func folderRole(mailboxes []MailboxInfo, name string) string {
	for i := range mailboxes {
		if mailboxes[i].Name() == name {
			return mailboxes[i].role()
		}
	}
	return ""
}

func searchFolders(mailboxes []MailboxInfo, q search.Query) []string {
	named, everywhere := q.Scope()
	excluded := q.Excluded()
	var out []string
	for i := range mailboxes {
		mbox := &mailboxes[i]
		if mbox.IsInternal() || slices.ContainsFunc(excluded, func(name string) bool { return folderNamed(mbox, name) }) {
			continue
		}
		role := mbox.role()
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
	role := mbox.role()
	last := mbox.Name()[strings.LastIndex(mbox.Name(), string(mbox.Delim))+1:]
	return strings.EqualFold(named, role) || strings.EqualFold(named, mbox.Name()) || strings.EqualFold(named, last) ||
		(role == "junk" && strings.EqualFold(named, "spam"))
}

// searchWindow is a search's rows up to the page asked, from every folder it spans, merged as its page is.
func (p *plugin) searchWindow(ctx context.Context, logins []*alborz.Login, ask listAsk, settings *Settings) (*listingEntry, []string, error) {
	spec := ask.spec
	q := search.Parse(spec.query)
	class, bound := listingBudget(spec)
	return p.gather(ctx, logins, spec, func(s *alborz.Login) (*listingEntry, error) {
		sb, err := p.sidebarFor(s)
		if err != nil {
			return nil, err
		}
		// One turn per folder, so the account's other pages are served between them.
		found := &listingEntry{sortSupported: true}
		for _, folder := range searchFolders(sb.mailboxes, q) {
			e, err := readOn(s, class, bound, func(c *imapclient.Client) (*listingEntry, error) {
				return p.fetchUnifiedAccount(c, s.Username(), folder, spec, settings, ask.page.Window(), listFetchOptions, false)
			})(ctx)
			if err != nil {
				return nil, err
			}
			found.absorb(e)
		}
		return found, nil
	})
}
