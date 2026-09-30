package web

import (
	"context"
	"fmt"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

// remind pushes the alarms due since the last round to the devices of each account signed in and
// unlocked (ADR 74); nothing reads a calendar for an account with no login alive.
func (p *plugin) remind(srv *web.Server) func(context.Context, time.Time) {
	var last time.Time
	return func(ctx context.Context, now time.Time) {
		from := last
		if from.IsZero() {
			from = now.Add(-srv.Options.ReminderTick)
		}
		last = now
		for _, login := range srv.Logins() {
			account := login.Username()
			if devices, err := srv.Pushes.Devices(account); err != nil || len(devices) == 0 || login.Locked() {
				continue
			}
			reminders, err := p.Reminders(alborz.Background(ctx), login, zoneOf(ctx, login), from, now)
			if err != nil {
				srv.Logger().Printf("reminders %s: %v", account, err)
			}
			for _, r := range reminders {
				path, err := srv.Path("calendar.event", r.Path)
				if err != nil {
					srv.Logger().Printf("reminders %s: %v", account, err)
					continue
				}
				item := fmt.Sprintf("event %s %s %s", r.UID, r.Start.UTC().Format(time.RFC3339), r.At.UTC().Format(time.RFC3339))
				srv.Push(account, web.PushNote{Key: "push.event", Item: item, At: r.Start, Path: path, Urgent: true})
			}
		}
	}
}

// zoneOf is the reader's zone, for times written without one; UTC where the profile names none.
func zoneOf(ctx context.Context, login *alborz.Login) *time.Location {
	var profile alborz.Profile
	if login.Store().Get(ctx, alborz.ProfileKey, &profile) != nil {
		return time.UTC
	}
	loc, err := time.LoadLocation(profile.Reading.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}
