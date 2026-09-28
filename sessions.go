package alborz

import (
	"net/netip"
	"slices"
	"strings"
	"time"
)

// Signed is one browser signed into an account.
type Signed struct {
	ID string
	// Device is the browser's own User-Agent claim; Address is "" when it was a
	// proxy's address.
	Device, Address string
	Created, Seen   time.Time
	Current         bool
}

// note records where the browser is, for display only, and reports a change.
func (s *Session) note(device, address string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.device == device && s.address == address {
		return false
	}
	s.device, s.address = device, address
	return true
}

func (s *Session) holds(account string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.remember[account]; ok {
		return true
	}
	return slices.ContainsFunc(s.accounts, func(login *Login) bool { return login.username == account && login.alive() })
}

func (s *Session) signed() Signed {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Signed{ID: s.ID, Device: s.device, Address: s.address, Created: s.Created, Seen: s.seen}
}

// requestAddress is "" for loopback and private addresses: an untrusted proxy
// in front of alborz, which says nothing about the reader.
func requestAddress(from string) string {
	ip, err := netip.ParseAddr(from)
	if err != nil || !publicAddr(ip) {
		return ""
	}
	return from
}

// SignedIn lists every browser signed into the account, live and remembered,
// newest use first.
func (ss *Sessions) SignedIn(account, current string) ([]Signed, error) {
	seen := map[string]bool{}
	var out []Signed
	ss.mu.Lock()
	for id, s := range ss.live {
		// Expired sessions are only dropped when their browser returns.
		if time.Since(s.lastSeen()) > s.life() {
			continue
		}
		if s.holds(account) {
			seen[id] = true
			out = append(out, s.signed())
		}
	}
	ss.mu.Unlock()
	err := ss.records.Each(func(rec *SessionRecord) {
		if seen[rec.ID] || !slices.ContainsFunc(rec.Accounts, func(a RememberedAccount) bool { return a.Address == account }) {
			return
		}
		out = append(out, Signed{ID: rec.ID, Device: rec.Device, Address: rec.Address, Created: rec.Created, Seen: rec.Seen})
	})
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Current = out[i].ID == current
	}
	slices.SortFunc(out, func(a, b Signed) int { return b.Seen.Compare(a.Seen) })
	return out, nil
}

// SignOut takes the account out of one browser; a browser left with none is forgotten.
func (ss *Sessions) SignOut(id, account string) error {
	ss.mu.Lock()
	s := ss.live[id]
	ss.mu.Unlock()
	if s != nil {
		if s := s.Remove(account); s != nil {
			s.Close()
		}
		s.forget(account)
		if live, _ := s.Accounts(); len(live) == 0 && !s.remembered() {
			ss.Delete(id)
			return nil
		}
		return ss.Save(s)
	}
	rec, ok := ss.records.Load(id)
	if !ok {
		return nil
	}
	rec.Accounts = slices.DeleteFunc(rec.Accounts, func(a RememberedAccount) bool { return a.Address == account })
	if len(rec.Accounts) == 0 {
		return ss.records.Delete(id)
	}
	return ss.records.Save(rec)
}

// Device is a User-Agent reduced to a browser and a system, each "" when unrecognised.
type Device struct{ Browser, System string }

func DeviceName(ua string) Device {
	var browser, system string
	for _, b := range []struct{ mark, name string }{
		{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"},
	} {
		if strings.Contains(ua, b.mark) {
			browser = b.name
			break
		}
	}
	for _, s := range []struct{ mark, name string }{
		{"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Android", "Android"}, {"Mac OS X", "macOS"},
		{"Windows", "Windows"}, {"CrOS", "ChromeOS"}, {"Linux", "Linux"},
	} {
		if strings.Contains(ua, s.mark) {
			system = s.name
			break
		}
	}
	return Device{browser, system}
}
