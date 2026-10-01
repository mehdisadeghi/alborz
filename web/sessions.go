package web

import (
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
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
	return slices.ContainsFunc(s.accounts, func(login *alborz.Login) bool { return login.Username() == account && login.Alive() })
}

func (s *Session) signed() Signed {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Signed{ID: s.ID, Device: s.device, Address: s.address, Created: s.created, Seen: s.seen}
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
	err := ss.records.Each(func(rec *alborz.SessionRecord) {
		if seen[rec.ID] || !slices.ContainsFunc(rec.Accounts, func(a alborz.RememberedAccount) bool { return a.Address == account }) {
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

// Holds says whether the browser of that session is signed into the account, live or remembered.
func (ss *Sessions) Holds(id, account string) bool {
	if s := ss.get(id); s != nil && s.holds(account) {
		return true
	}
	rec, ok := ss.records.Load(id)
	// A record outlives its cookie until the next start sweeps it.
	return ok && time.Since(rec.Seen) <= alborz.CredentialCookieLife() &&
		slices.ContainsFunc(rec.Accounts, func(a alborz.RememberedAccount) bool { return a.Address == account })
}

// holdsLogin says whether the browser of that session holds the login itself.
func (ss *Sessions) holdsLogin(id string, login *alborz.Login) bool {
	s := ss.get(id)
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.accounts, login)
}

// SignOut takes the account out of one browser, and its notifications with it; a browser left with none
// is forgotten.
func (ss *Sessions) SignOut(id, account string) error {
	if err := ss.pushes.SignedOut(account, id); err != nil {
		return err
	}
	ss.mu.Lock()
	s := ss.live[id]
	ss.mu.Unlock()
	if s != nil {
		if s := s.remove(account); s != nil {
			s.Close()
		}
		s.forget(account)
		if live, _ := s.Accounts(); len(live) == 0 && !s.remembered() {
			ss.delete(id)
			return nil
		}
		return ss.Save(s)
	}
	rec, ok := ss.records.Load(id)
	if !ok {
		return nil
	}
	rec.Accounts = slices.DeleteFunc(rec.Accounts, func(a alborz.RememberedAccount) bool { return a.Address == account })
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

// PasswordChanged puts login, signed in with the account's new password, in place of the
// account's login in this browser and keeps the password where the old one was remembered.
// Every other browser is signed out of the account: what it remembers opens with the old
// password only, and it holds no secret of this browser to reseal it with.
func (ctx *Context) PasswordChanged(login *alborz.Login, password string) error {
	s, account := ctx.Session(), login.Username()
	s.mu.Lock()
	_, remembered := s.remember[account]
	s.mu.Unlock()
	ctx.AddAccount(login)
	if remembered {
		ctx.SetLoginToken(account, password)
	}
	signed, err := ctx.Server.Sessions.SignedIn(account, s.ID)
	if err != nil {
		return err
	}
	for _, other := range signed {
		if other.Current {
			continue
		}
		if err := ctx.Server.Sessions.SignOut(other.ID, account); err != nil {
			return err
		}
	}
	return nil
}
