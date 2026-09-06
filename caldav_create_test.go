package alborz_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A brand-new account has no calendars, so creating the first one must
// not be refused for want of one already there.
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

	form := url.Values{"name": {"First"}, "color": {"#22aa55"}, "holds": {"both"}}
	resp := postForm(t, c, base+"/calendars/create", form)
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
