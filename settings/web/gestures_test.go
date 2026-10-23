package web_test

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"git.mehdix.org/alborz"
	. "git.mehdix.org/alborz/internal/alborztest"
)

func swipePreferences(t *testing.T, page string) alborz.Gestures {
	t.Helper()
	m := regexp.MustCompile(`id="swipe-preferences"[^>]*data-swipes="([^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("the page carries no swipe preferences")
	}
	var g alborz.Gestures
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &g); err != nil {
		t.Fatalf("swipe preferences: %v", err)
	}
	return g
}

func drawnProfile(t *testing.T, page string) string {
	t.Helper()
	m := regexp.MustCompile(`name="shown" value="([^"]*)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("the form carries no shown profile")
	}
	return html.UnescapeString(m[1])
}

func swipeSelections(page string) url.Values {
	values := url.Values{}
	for _, selectTag := range regexp.MustCompile(`(?s)<select name="(swipe_[^"]+)"[^>]*>(.*?)</select>`).FindAllStringSubmatch(page, -1) {
		if option := regexp.MustCompile(`value="([^"]*)" selected`).FindStringSubmatch(selectTag[2]); option != nil {
			values.Set(selectTag[1], option[1])
		}
	}
	return values
}

func TestGesturePreferencesSaveResetAndRender(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	page := c.Page(t, "settings.profile")
	if got := swipePreferences(t, page); got != (alborz.Gestures{}).Resolved() {
		t.Fatalf("initial gestures = %#v", got)
	}
	if main, preferences := strings.Index(page, `<main id="main"`), strings.Index(page, `id="swipe-preferences"`); main < 0 || preferences < main || preferences > strings.Index(page, "</main>") {
		t.Fatal("swipe preferences are not inside main")
	}
	if keys, gestures := strings.Index(page, "keys.js"), strings.Index(page, "gestures.js"); keys < 0 || gestures <= keys {
		t.Fatal("gestures.js does not load after keys.js")
	}
	for _, attr := range []string{`data-gesture-toggle="rows"`, `data-gesture-toggle="navigation"`, `data-gesture-reset`} {
		if !strings.Contains(page, attr) {
			t.Errorf("settings missing %s", attr)
		}
	}
	for _, tag := range regexp.MustCompile(`<input[^>]*data-gesture-toggle[^>]*>`).FindAllString(page, -1) {
		if strings.Contains(tag, "name=") {
			t.Errorf("browser-local toggle would be submitted: %s", tag)
		}
	}
	want := alborz.Gestures{
		Mail: alborz.SwipeActions{Left: "none", Right: "junk"}, Tasks: alborz.SwipeActions{Left: "star", Right: "none"},
		Contacts: alborz.SwipeActions{Left: "star", Right: "none"}, Journal: alborz.SwipeActions{Left: "none", Right: "delete"},
		Events: alborz.SwipeActions{Left: "star", Right: "none"},
	}
	form := url.Values{
		"swipe_mail_left": {"none"}, "swipe_mail_right": {"junk"}, "swipe_tasks_left": {"star"}, "swipe_tasks_right": {"none"},
		"swipe_contacts_left": {"star"}, "swipe_contacts_right": {"none"}, "swipe_journal_left": {"none"}, "swipe_journal_right": {"delete"},
		"swipe_events_left": {"star"}, "swipe_events_right": {"none"},
	}
	if resp := c.Post(t, "settings.profile", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving gestures: %s", resp.Status)
	}
	page = c.Page(t, "settings.profile")
	if got := swipePreferences(t, page); got != want {
		t.Fatalf("saved gestures = %#v, want %#v", got, want)
	}
	if got := swipeSelections(page); !reflect.DeepEqual(got, form) {
		t.Fatalf("settings selections = %v, want %v", got, form)
	}
	var stored alborz.Profile
	if err := json.Unmarshal([]byte(drawnProfile(t, page)), &stored); err != nil || stored.Gestures != want {
		t.Fatalf("saved form lost explicit choices: %#v, %v", stored.Gestures, err)
	}
	req, err := http.NewRequest(http.MethodGet, a.URL+a.Path("mail.mailbox", "INBOX"), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "main")
	resp := c.Do(t, req)
	if resp.StatusCode != http.StatusOK || swipePreferences(t, resp.Text) != want {
		t.Fatalf("main swap lost preferences: %s", resp.Status)
	}
	for key := range form {
		form.Set(key, "")
	}
	if resp := c.Post(t, "settings.profile", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("resetting gestures: %s", resp.Status)
	}
	page = c.Page(t, "settings.profile")
	if got := swipePreferences(t, page); got != (alborz.Gestures{}).Resolved() {
		t.Fatalf("reset gestures = %#v", got)
	}
	if got := swipeSelections(page); !reflect.DeepEqual(got, form) {
		t.Fatalf("reset selections = %v, want %v", got, form)
	}
}

func TestGesturePreferencesRejectUnsupportedActions(t *testing.T) {
	for _, field := range []string{"swipe_mail_left", "swipe_tasks_right", "swipe_contacts_left", "swipe_journal_right", "swipe_events_left"} {
		t.Run(field, func(t *testing.T) {
			c := Alborz(t).SignIn(t, User)
			resp := c.Post(t, "settings.profile", url.Values{field: {"unsupported"}})
			if resp.StatusCode != http.StatusUnprocessableEntity || !Says(t, NoticeBar(resp.Text), "form.swipeaction") {
				t.Fatalf("unsupported action: %s\n%s", resp.Status, NoticeBar(resp.Text))
			}
			if got := swipePreferences(t, c.Page(t, "settings.profile")); got != (alborz.Gestures{}).Resolved() {
				t.Fatalf("invalid action changed the profile: %#v", got)
			}
		})
	}
}

func TestGesturePreferencesMergeIndependentEdits(t *testing.T) {
	a := Alborz(t)
	first, second := a.SignIn(t, User), a.SignIn(t, User)
	before := drawnProfile(t, first.Page(t, "settings.profile"))
	for _, edit := range []struct {
		c            *Client
		field, value string
	}{{first, "swipe_mail_left", "none"}, {second, "swipe_tasks_right", "star"}} {
		if resp := edit.c.Post(t, "settings.profile", url.Values{"shown": {before}, edit.field: {edit.value}}); resp.StatusCode != http.StatusFound {
			t.Fatalf("saving %s: %s", edit.field, resp.Status)
		}
	}
	want := (alborz.Gestures{Mail: alborz.SwipeActions{Left: "none"}, Tasks: alborz.SwipeActions{Right: "star"}}).Resolved()
	if got := swipePreferences(t, first.Page(t, "settings.profile")); !reflect.DeepEqual(got, want) {
		t.Fatalf("independent edits = %#v, want %#v", got, want)
	}
}

func TestSigningInAskedAboutDifferentGestures(t *testing.T) {
	a := Alborz(t)
	if resp := a.SignIn(t, User).Post(t, "settings.profile", url.Values{"swipe_mail_left": {"none"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving gestures: %s", resp.Status)
	}
	c := a.SignIn(t, User2)
	resp := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}}, url.Values{"add": {"1"}})
	bar := NoticeBar(c.Follow(t, resp.Header.Get("Location")))
	if !Says(t, bar, "gestures.title") {
		t.Fatalf("no notice names differing gestures:\n%s", bar)
	}
}
