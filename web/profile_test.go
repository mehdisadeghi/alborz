package web

import (
	"testing"

	"git.mehdix.org/alborz"
)

// The profile is the person's, so All accounts reads the zone one account's page does.
func TestMergedScopeReadsThePersonsZone(t *testing.T) {
	server := rememberedServer(t, nil)
	ctx := rememberedContext(t, server)
	for _, account := range []string{"one@test.local", "two@test.local"} {
		login, err := server.SignIn(account, "password", "")
		if err != nil {
			t.Fatal(err)
		}
		ctx.session.add(login)
	}
	ctx.session.setProfile(alborz.Profile{Reading: alborz.Reading{Timezone: "Asia/Tehran"}})
	zone := ""
	err := Accounts(func(ctx *Context) error {
		if !ctx.Unified {
			t.Fatal("two accounts with none named are not the merged scope")
		}
		zone = UserLocation(ctx).String()
		return nil
	})(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if zone != "Asia/Tehran" {
		t.Errorf("the merged scope reads %q", zone)
	}
}
