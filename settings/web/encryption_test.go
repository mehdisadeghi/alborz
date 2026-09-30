package web_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/internal/seal"
)

var recoveryKey = regexp.MustCompile(`<p class="recovery-key" dir="ltr"><code>([A-Z2-7-]+)</code>`)

// encrypt turns User's encryption on and is the recovery key the page shows once.
func encrypt(t *testing.T, c *Client, password string) string {
	t.Helper()
	resp := c.Post(t, "settings.encryption", url.Values{"password": {password}, "account": {User}})
	m := recoveryKey.FindStringSubmatch(resp.Text)
	if resp.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("turning encryption on answered %s with no recovery key:\n%s", resp.Status, NoticeBar(resp.Text))
	}
	return m[1]
}

func signInWith(t *testing.T, a *App, password string) (*Client, string) {
	t.Helper()
	c := a.Client()
	resp := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {password}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("signing in answered %s", resp.Status)
	}
	return c, c.Follow(t, resp.Header.Get("Location"))
}

// sealedBy counts, in the data file of a stopped alborz, the values of User's collections, settings and key
// pair by the kind of key that sealed them.
func sealedBy(t *testing.T, dir string) map[byte]int {
	t.Helper()
	db, err := bolt.Open(filepath.Join(dir, "alborz.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	counts := map[byte]int{}
	err = db.View(func(tx kv.Tx) error {
		for _, prefix := range []string{"alborz.pim/collections/", "alborz.pim/objects/", "alborz.settings/"} {
			err := tx.Scan(prefix, func(k string, v []byte) error {
				if !strings.HasPrefix(k, "alborz.settings/key/") {
					counts[seal.Format(v)]++
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return tx.Scan("alborz.keys/", func(_ string, v []byte) error {
			var pair struct{ Private []byte }
			err := json.Unmarshal(v, &pair)
			counts[seal.Format(pair.Private)]++
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

// Turned on, off and on again with data present, every value of the account's is under the key the state
// asks for, and reads back after a restart: through a browser, and through a phone with no browser signed in.
func TestEncryptionTurnsOnOffAndOnAgain(t *testing.T) {
	imap, dir := IMAP(t), t.TempDir()
	opts := []Option{WithIMAP(imap), DataDir(dir)}
	a := Alborz(t, opts...)
	c := a.SignIn(t, User)
	cal := c.CreateCalendar(t, "Personal", "")
	a.Put(t, cal+"review.ics", "text/calendar", Event("review", "Budget review"))
	if resp := c.Post(t, "settings.profile", url.Values{"messages_per_page": {"17"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the profile: %s", resp.Status)
	}
	first := encrypt(t, c, Password)
	a.Close()
	if got := sealedBy(t, dir); got[seal.ByInstance] != 0 || got[seal.ByAccount] < 4 {
		t.Fatalf("turned on, values by kind of key: %v, want all under the account's", got)
	}

	a = Alborz(t, opts...)
	if got := a.DAV(t, http.MethodGet, cal+"review.ics", nil, "").Text; got != Event("review", "Budget review") {
		t.Fatalf("a phone with no browser signed in read %q", got)
	}
	c = a.SignIn(t, User)
	if !c.Listed(t)[Here+cal] || perPage(t, c) != "17" {
		t.Fatal("signed in again, the calendar or the settings do not read")
	}

	if resp := c.Post(t, "settings.encryption.off", url.Values{"password": {"wrong"}, "account": {User}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a wrong password turning encryption off answered %s", resp.Status)
	}
	if resp := c.Post(t, "settings.encryption.off", url.Values{"password": {Password}, "account": {User}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("turning encryption off answered %s", resp.Status)
	}
	a.Close()
	if got := sealedBy(t, dir); got[seal.ByAccount] != 0 || got[seal.ByInstance] < 4 {
		t.Fatalf("turned off, values by kind of key: %v, want all under the instance's", got)
	}

	a = Alborz(t, opts...)
	c = a.SignIn(t, User)
	again := encrypt(t, c, Password)
	if again == first {
		t.Fatal("turned on again, the recovery key is the one shown before")
	}
	if !c.Listed(t)[Here+cal] {
		t.Fatal("turned on again, the calendar is gone")
	}
	a.Close()
	if got := sealedBy(t, dir); got[seal.ByInstance] != 0 {
		t.Fatalf("turned on again, values by kind of key: %v", got)
	}

	// Turned on again, the data is under a new key: the first recovery key opens none of it.
	imap.SetPassword(User, "reset")
	a = Alborz(t, opts...)
	c, _ = signInWith(t, a, "reset")
	if resp := c.Post(t, "settings.encryption.recover", url.Values{"recovery": {first}, "account": {User}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("the first recovery key answered %s", resp.Status)
	}
	if resp := c.Post(t, "settings.encryption.recover", url.Values{"recovery": {again}, "account": {User}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("the current recovery key answered %s", resp.Status)
	}
	if !c.Listed(t)[Here+cal] {
		t.Fatal("opened with the current recovery key, the calendar does not read")
	}
}

// A password changed in Settings wraps the key under the new one: after a restart, with nothing holding the
// key, the new password opens the data. The key is kept on the mail server here, as METADATA allows.
func TestEncryptedDataFollowsAPasswordChange(t *testing.T) {
	imap, dir := IMAP(t, Metadata()), t.TempDir()
	opts := []Option{WithIMAP(imap), WithPasswd(Passwd(t, imap)), DataDir(dir)}
	a := Alborz(t, opts...)
	c := a.SignIn(t, User)
	cal := c.CreateCalendar(t, "Personal", "")
	encrypt(t, c, Password)
	if page := c.Page(t, "settings.encryption", url.Values{"account": {User}}); !strings.Contains(page, "On the mail server") {
		t.Fatal("the page does not say the key is kept on the mail server")
	}
	if resp := c.Post(t, "settings.password", change(Password, "fresh-secret", "fresh-secret")); resp.StatusCode != http.StatusFound {
		t.Fatalf("changing the password: %s", resp.Status)
	}
	a.Close()

	a = Alborz(t, opts...)
	c, page := signInWith(t, a, "fresh-secret")
	if strings.Contains(NoticeBar(page), "does not open") || !c.Listed(t)[Here+cal] {
		t.Fatalf("the new password does not open the data:\n%s", NoticeBar(page))
	}
}

// A password reset outside alborz leaves the data shut; the sign-in says so, and only the recovery key opens it.
func TestTheRecoveryKeyOpensAfterAReset(t *testing.T) {
	imap, dir := IMAP(t), t.TempDir()
	opts := []Option{WithIMAP(imap), DataDir(dir)}
	a := Alborz(t, opts...)
	c := a.SignIn(t, User)
	cal := c.CreateCalendar(t, "Personal", "")
	recovery := encrypt(t, c, Password)
	a.Close()

	imap.SetPassword(User, "reset")
	a = Alborz(t, opts...)
	c, page := signInWith(t, a, "reset")
	if !strings.Contains(NoticeBar(page), "does not open with this password") {
		t.Fatalf("the sign-in does not say the data is shut:\n%s", NoticeBar(page))
	}
	if c.Listed(t)[Here+cal] {
		t.Fatal("the calendar reads without its key")
	}
	resp := c.Post(t, "settings.profile", url.Values{"messages_per_page": {"17"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(NoticeBar(resp.Text), "encrypted and not open") {
		t.Fatalf("a settings change while shut answered %s:\n%s", resp.Status, NoticeBar(resp.Text))
	}
	if resp := c.Post(t, "settings.encryption.recover", url.Values{"recovery": {"AAAA-BBBB"}, "account": {User}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a wrong recovery key answered %s", resp.Status)
	}
	typed := strings.ToLower(strings.ReplaceAll(recovery, "-", ""))
	if resp := c.Post(t, "settings.encryption.recover", url.Values{"recovery": {typed}, "account": {User}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("the recovery key answered %s", resp.Status)
	}
	if !c.Listed(t)[Here+cal] {
		t.Fatal("opened with the recovery key, the calendar does not read")
	}
	a.Close()

	a = Alborz(t, opts...)
	if c, _ := signInWith(t, a, "reset"); !c.Listed(t)[Here+cal] {
		t.Fatal("after the recovery, the reset password does not open the data")
	}
}

// Without the old password or the recovery key the data is deleted only when the reader confirms it.
func TestStartingOverDeletesOnlyWhenConfirmed(t *testing.T) {
	imap, dir := IMAP(t), t.TempDir()
	opts := []Option{WithIMAP(imap), DataDir(dir)}
	a := Alborz(t, opts...)
	c := a.SignIn(t, User)
	c.CreateCalendar(t, "Personal", "")
	encrypt(t, c, Password)
	a.Close()

	imap.SetPassword(User, "reset")
	a = Alborz(t, opts...)
	c, _ = signInWith(t, a, "reset")
	if resp := c.Post(t, "settings.encryption.startover", url.Values{"account": {User}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("starting over unconfirmed answered %s", resp.Status)
	}
	a.Close()
	if got := sealedBy(t, dir); got[seal.ByAccount] < 2 {
		t.Fatalf("unconfirmed, the data under the lost key is gone: %v", got)
	}

	a = Alborz(t, opts...)
	c, _ = signInWith(t, a, "reset")
	resp := c.Post(t, "settings.encryption.startover", url.Values{"confirm": {"1"}, "account": {User}})
	if resp.StatusCode != http.StatusOK || recoveryKey.FindStringSubmatch(resp.Text) == nil {
		t.Fatalf("starting over answered %s with no new recovery key", resp.Status)
	}
	cal := c.CreateCalendar(t, "Fresh", "")
	a.Close()
	if got := sealedBy(t, dir); got[seal.ByInstance] != 0 {
		t.Fatalf("after starting over, values by kind of key: %v, want all under the new key", got)
	}

	a = Alborz(t, opts...)
	c, page := signInWith(t, a, "reset")
	listed := c.Listed(t)
	if strings.Contains(NoticeBar(page), "does not open") || !listed[Here+cal] || listed[Here+CalendarsOf(User)+"personal/"] {
		t.Fatalf("after starting over, listed %v:\n%s", listed, NoticeBar(page))
	}
}

// Starting over on one alborz leaves another's data readable: one whose login still holds the old key, and
// one given the old recovery key, seal it again under the new key (ADR 72).
func TestStartingOverElsewhereLeavesAnInstancesDataReadable(t *testing.T) {
	imap := IMAP(t, Metadata())
	instance := func(dir string) *App { return Alborz(t, WithIMAP(imap), DataDir(dir)) }
	heldDir, recoveredDir := t.TempDir(), t.TempDir()

	held := instance(heldDir)
	holder := held.SignIn(t, User)
	oldRecovery := encrypt(t, holder, Password)
	heldCal := holder.CreateCalendar(t, "Held", "")

	recovered := instance(recoveredDir)
	recoveredCal := recovered.SignIn(t, User).CreateCalendar(t, "Recovered", "")
	recovered.Close()

	imap.SetPassword(User, "reset")
	other := instance(t.TempDir())
	c, _ := signInWith(t, other, "reset")
	if resp := c.Post(t, "settings.encryption.startover", url.Values{"confirm": {"1"}, "account": {User}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("starting over answered %s", resp.Status)
	}

	c, page := signInWith(t, held, "reset")
	if strings.Contains(NoticeBar(page), "does not open") || !c.Listed(t)[Here+heldCal] {
		t.Fatalf("the instance holding the old key does not read its data:\n%s", NoticeBar(page))
	}
	held.Close()
	held = instance(heldDir)
	if c, _ := signInWith(t, held, "reset"); !c.Listed(t)[Here+heldCal] {
		t.Fatal("after a restart, the data sealed again under the new key does not read")
	}

	recovered = instance(recoveredDir)
	c, page = signInWith(t, recovered, "reset")
	if !strings.Contains(NoticeBar(page), "does not open") || c.Listed(t)[Here+recoveredCal] {
		t.Fatalf("under the old key only, the data reads or the sign-in says nothing:\n%s", NoticeBar(page))
	}
	if resp := c.Post(t, "settings.encryption.recover", url.Values{"recovery": {oldRecovery}, "account": {User}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("the old recovery key answered %s", resp.Status)
	}
	recovered.Close()
	recovered = instance(recoveredDir)
	if c, _ := signInWith(t, recovered, "reset"); !c.Listed(t)[Here+recoveredCal] {
		t.Fatal("given the old recovery key, the data is not sealed under the new key")
	}
}

// A share from an encrypted account to one that has not signed in opens after the owner's next sign-in; a
// sharee without encryption then reads it with no owner signed in, as the page says the server can.
func TestAShareFromAnEncryptedToAPlainAccount(t *testing.T) {
	imap, dir := IMAP(t), t.TempDir()
	opts := []Option{WithIMAP(imap), DataDir(dir)}
	a := Alborz(t, opts...)
	owner := a.SignIn(t, User)
	encrypt(t, owner, Password)
	cal := owner.CreateCalendar(t, "Plans", "")
	if resp := owner.Post(t, "calendar.collection.share", url.Values{"to": {User2}, "access": {"read"}}, Here+cal); resp.StatusCode != http.StatusFound {
		t.Fatalf("sharing answered %s", resp.Status)
	}
	a.Close()

	a = Alborz(t, opts...)
	guest := a.SignIn(t, User2)
	guestPath := Here + CalendarsOf(User2) + "plans~" + User + "/"
	if resp := guest.Post(t, "calendar.collection.accept", url.Values{}, guestPath); resp.StatusCode == http.StatusFound {
		t.Fatal("the share opened before the owner came back")
	}
	a.SignIn(t, User)
	if resp := guest.Post(t, "calendar.collection.accept", url.Values{}, guestPath); resp.StatusCode != http.StatusFound {
		t.Fatalf("after the owner's sign-in, accepting answered %s", resp.Status)
	}
	a.Close()

	a = Alborz(t, opts...)
	if !a.SignIn(t, User2).Listed(t)[guestPath] {
		t.Fatal("with the owner signed out, the plain sharee does not read the calendar")
	}
}

// A mail server that will not keep the key says so on the form, in its own words.
func TestAKeyTheServerWillNotKeepIsRefusedOnTheForm(t *testing.T) {
	imap := IMAP(t, Metadata())
	c := Alborz(t, WithIMAP(imap)).SignIn(t, User)
	imap.RefuseEntries()
	resp := c.Post(t, "settings.encryption", url.Values{"password": {Password}, "account": {User}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("turning encryption on answered %s, want 422", resp.Status)
	}
	if bar := NoticeBar(resp.Text); !strings.Contains(bar, "The change was not saved. The server said:") || !strings.Contains(bar, "too many entries") {
		t.Errorf("the form does not give the server's refusal:\n%s", bar)
	}
	if recoveryKey.MatchString(resp.Text) {
		t.Error("the refused form shows a recovery key")
	}
}

// A key on the server that does not decode signs in locked, says why, and is never written over.
func TestAKeyThatDoesNotReadSignsInLocked(t *testing.T) {
	imap := IMAP(t, Metadata())
	imap.SetEntry(User, "key", "not json")
	c, page := signInWith(t, Alborz(t, WithIMAP(imap)), Password)
	if bar := NoticeBar(page); !strings.Contains(bar, "The key to the data alborz keeps for "+User+" could not be read.") {
		t.Errorf("the page after signing in does not say why the account is locked:\n%s", bar)
	}
	if resp := c.Get(t, "settings.encryption", url.Values{"account": {User}}); resp.StatusCode != http.StatusOK {
		t.Errorf("the encryption page answered %s", resp.Status)
	}
	for route, form := range map[string]url.Values{
		"settings.encryption":           {"password": {Password}},
		"settings.encryption.startover": {"confirm": {"1"}},
	} {
		form.Set("account", User)
		if resp := c.Post(t, route, form); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s answered %s, want 422", route, resp.Status)
		}
	}
	if got := imap.Entry(User, "key"); got != "not json" {
		t.Errorf("the key was written over with %q", got)
	}
}
