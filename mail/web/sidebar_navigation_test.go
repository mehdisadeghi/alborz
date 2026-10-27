package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestMailSearchRetainsSidebarOnNavigationAndHistory(t *testing.T) {
	a, c := SignedIn(t, User)
	for _, accounts := range []string{"single", "merged"} {
		if accounts == "merged" {
			c.SignIn(t, User2)
		}
		for _, query := range []url.Values{{"view": {"starred"}}, {"view": {"red"}}, {"query": {"hello"}}} {
			for _, navigation := range []string{"direct", "boosted", "history"} {
				t.Run(accounts+"/"+query.Encode()+"/"+navigation, func(t *testing.T) {
					req, err := http.NewRequest(http.MethodGet, a.URL+a.Path("mail.search", query), nil)
					if err != nil {
						t.Fatal(err)
					}
					switch navigation {
					case "boosted":
						req.Header.Set("HX-Request", "true")
						req.Header.Set("HX-Boosted", "true")
						req.Header.Set("HX-Target", "body")
					case "history":
						req.Header.Set("HX-History-Restore-Request", "true")
					}
					response := c.Do(t, req)
					if response.StatusCode != http.StatusOK {
						t.Fatalf("search: %s", response.Status)
					}
					if strings.Count(response.Text, "data-mail-sidebar=") != 1 {
						t.Fatal("search navigation lost the mail sidebar")
					}
					if !strings.Contains(response.Text, `data-rail-key="views:`) {
						t.Fatal("search sidebar lost starred and color navigation")
					}
				})
			}
		}
	}
}
