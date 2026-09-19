package alborz

import (
	"net/netip"
	"slices"
	"strings"
	"time"
)

// Signed is one browser signed into an account, as the account's list
// of them shows it.
type Signed struct {
	ID string
	// Device is the browser's User-Agent, which the browser writes and
	// can be told to write anything; Address is where it last came
	// from, empty when that is a proxy's own address and says nothing.
	Device, Address string
	Created, Seen   time.Time
	// Current is the browser asking.
	Current bool
}

// note keeps what a request says about where the browser is: shown to
// the account holder, never used to decide anything. It reports a
// change, which a remembered visit writes down.
func (v *Visit) note(device, address string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.device == device && v.address == address {
		return false
	}
	v.device, v.address = device, address
	return true
}

// holds reports whether the visit is signed into the account, now or
// by a remembered password.
func (v *Visit) holds(account string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.remember[account]; ok {
		return true
	}
	return slices.ContainsFunc(v.accounts, func(s *Session) bool { return s.username == account && s.alive() })
}

func (v *Visit) signed() Signed {
	v.mu.Lock()
	defer v.mu.Unlock()
	return Signed{ID: v.ID, Device: v.device, Address: v.address, Created: v.Created, Seen: v.seen}
}

// requestAddress is where a request came from, for the account holder
// to recognise, and nothing when that is a loopback or private address:
// a proxy in front of alborz that was not named as trusted, which says
// nothing about the reader.
func requestAddress(from string) string {
	ip, err := netip.ParseAddr(from)
	if err != nil || !publicAddr(ip) {
		return ""
	}
	return from
}

// SignedIn is every browser signed into the account: those in use now
// and those remembered on disk, newest use first.
func (vs *Visits) SignedIn(account, current string) ([]Signed, error) {
	seen := map[string]bool{}
	var out []Signed
	vs.mu.Lock()
	for id, v := range vs.live {
		// A visit past its life is dropped only when its browser comes
		// back, which one that was closed for good never does.
		if time.Since(v.lastSeen()) > v.life() {
			continue
		}
		if v.holds(account) {
			seen[id] = true
			out = append(out, v.signed())
		}
	}
	vs.mu.Unlock()
	if vs.records != nil {
		err := vs.records.Each(func(rec *VisitRecord) {
			if seen[rec.ID] || !slices.ContainsFunc(rec.Accounts, func(a RememberedAccount) bool { return a.Address == account }) {
				return
			}
			out = append(out, Signed{ID: rec.ID, Device: rec.Device, Address: rec.Address, Created: rec.Created, Seen: rec.Seen})
		})
		if err != nil {
			return nil, err
		}
	}
	for i := range out {
		out[i].Current = out[i].ID == current
	}
	slices.SortFunc(out, func(a, b Signed) int { return b.Seen.Compare(a.Seen) })
	return out, nil
}

// SignOut takes the account out of one browser, which keeps whatever
// else it is signed into; a browser left with no account is forgotten.
func (vs *Visits) SignOut(id, account string) error {
	vs.mu.Lock()
	v := vs.live[id]
	vs.mu.Unlock()
	if v != nil {
		if s := v.Remove(account); s != nil {
			s.Close()
		}
		v.forget(account)
		if live, _ := v.Accounts(); len(live) == 0 && !v.remembered() {
			vs.Delete(id)
			return nil
		}
		return vs.Save(v)
	}
	if vs.records == nil {
		return nil
	}
	rec, ok := vs.records.Load(id)
	if !ok {
		return nil
	}
	rec.Accounts = slices.DeleteFunc(rec.Accounts, func(a RememberedAccount) bool { return a.Address == account })
	if len(rec.Accounts) == 0 {
		return vs.records.Delete(id)
	}
	return vs.records.Save(rec)
}

// Device is a User-Agent said in two words, a browser and a system,
// each empty when it names none of the common ones. It is a reading of
// what the browser claims, for recognising it, and the page keeps the
// claim beside it.
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
