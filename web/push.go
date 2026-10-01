package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/dates"
	"git.mehdix.org/alborz/internal/locale"
	"git.mehdix.org/alborz/internal/webpush"
)

const (
	// pushTimeout bounds one post to a push service, so a slow one holds nothing up for long.
	pushTimeout = 10 * time.Second
	// pushTTL is how long a push service may hold a push for a device that is away; older than that, the
	// page tells the reader more than the notification would.
	pushTTL = time.Hour
)

// PushNote is what a push says, before it is put in a device's language (ADR 74).
type PushNote struct {
	// Key is the title's locale key; At, when set, is the time it names, in the device's zone.
	Key string
	// Item names what the push is about, so a device gets it at most once however often it is pushed.
	Item string
	At   time.Time
	// Path is the page a click opens, in the account.
	Path string
	// Replaces an undelivered push of the same key; Urgent asks the push service to wake the device;
	// Badge sets the app's badge.
	Replaces, Urgent, Badge bool
}

// keepLogin holds a login past its idle limit while a device of the browser holding it is to be notified.
func (s *Server) keepLogin(l *alborz.Login) bool {
	devices, err := s.Pushes.Devices(l.Username())
	if err != nil {
		s.Logger().Printf("push %s: %v", l.Username(), err)
		return false
	}
	for _, d := range devices {
		if s.Sessions.holdsLogin(d.Session, l) {
			return true
		}
	}
	return false
}

// Recheck lets the account's logins kept for notifications go, where nothing keeps them now.
func (s *Server) Recheck(account string) {
	for _, l := range s.Logins() {
		if l.Username() == account {
			l.Recheck()
		}
	}
}

// Push sends the note to every device of the account whose browser is still signed in to it.
func (s *Server) Push(account string, n PushNote) {
	devices, err := s.Pushes.Devices(account)
	if err != nil {
		s.Logger().Printf("push %s: %v", account, err)
		return
	}
	client := s.Outgoing.Remote(pushTimeout)
	for _, d := range devices {
		if s.Sessions.Holds(d.Session, account) {
			go s.pushTo(client, account, d, n)
		}
	}
}

func (s *Server) pushTo(client *http.Client, account string, d alborz.PushDevice, n PushNote) {
	if first, err := s.Pushes.Claim(d.Endpoint, n.Item, time.Now()); err != nil || !first {
		if err != nil {
			s.Logger().Printf("push %s: %v", account, err)
		}
		return
	}
	title := locale.T(d.Lang, n.Key)
	if !n.At.IsZero() {
		loc, err := time.LoadLocation(d.Zone)
		if err != nil {
			loc = time.UTC
		}
		title = locale.Tf(d.Lang, n.Key, dates.Reader{Lang: d.Lang, Loc: loc}.Clock(n.At))
	}
	payload, err := json.Marshal(map[string]any{"title": title, "body": account, "tag": n.Key + " " + account,
		"url": n.Path + AccountQuery(account), "badge": n.Badge})
	if err != nil {
		panic(err)
	}
	m := webpush.Message{Payload: payload, TTL: pushTTL}
	if n.Replaces {
		m.Topic = s.Pushes.Topic(account, n.Key)
	}
	if n.Urgent {
		m.Urgency = "high"
	}
	subject := d.Origin
	if s.PublicURL != nil {
		subject = s.PublicURL.Scheme + "://" + s.PublicURL.Host
	}
	status, err := webpush.Send(alborz.Background(context.Background()), client, s.Pushes.Key, subject, d.Subscription(), m)
	switch {
	case err != nil:
		s.Logger().Printf("push %s: %v", account, err)
	case status == http.StatusNotFound || status == http.StatusGone:
		// RFC 8030 section 7.3: the subscription is gone and will not come back.
		if err := s.Pushes.Remove(account, s.Pushes.ID(d)); err != nil {
			s.Logger().Printf("push %s: %v", account, err)
		}
	case status/100 != 2:
		s.Logger().Printf("push %s: the push service answered %d", account, status)
	}
}
