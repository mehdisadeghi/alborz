package alborz_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCreateCalendarWithNoneYet(t *testing.T) {
	stub := &davStub{
		cals:   map[string]davCal{},
		refuse: map[string]bool{},
		gone:   map[string]bool{},
	}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	base := startAlborzDav(t, startIMAP(t), srv.URL)
	c := login(t, base)

	page := get(t, c, base+"/calendar/create")
	if choice := regexp.MustCompile(`(?s)<select name="calendar"[^>]*>(.*?)</select>`).FindStringSubmatch(page); choice == nil || strings.Contains(choice[1], "<option") {
		t.Fatalf("the new-event form has no empty calendar choice")
	}
	resp, err := c.PostForm(base+"/calendar/create", url.Values{"summary": {"Lunch"}, "start": {"2026-09-28T12:00"}, "end": {"2026-09-28T13:00"}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "Choose where to save it.") {
		t.Fatalf("saving an event with nowhere to save answered %s, want the form refused", resp.Status)
	}

	form := url.Values{"name": {"First"}, "color": {"#22aa55"}, "holds": {"both"}, "place": {"domain"}}
	resp = postForm(t, c, base+"/calendars/create", form)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the first calendar answered %s, want a redirect", resp.Status)
	}
	if len(stub.cals) != 1 {
		t.Fatalf("the server was not asked to make the calendar: %d exist", len(stub.cals))
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(get(t, c, base+"/calendar"), "First") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("the created calendar never appeared in the rail")
}

// The guarded client refuses the loopback stub, which is the refusal under test.
func TestSubscribingToAnUnreadableFeedIsRefused(t *testing.T) {
	stub := &davStub{cals: map[string]davCal{}, refuse: map[string]bool{}, keep: map[string]bool{}, gone: map[string]bool{}}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	base := startAlborzDav(t, startIMAP(t), srv.URL)
	c := login(t, base)

	form := url.Values{"address": {srv.URL + "/feed.ics"}}
	resp := postForm(t, c, base+"/calendars/subscribe", form)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unreadable feed answered %s, want the form back", resp.Status)
	}
	if len(stub.cals) != 0 {
		t.Errorf("an address made a collection on the server")
	}
}
