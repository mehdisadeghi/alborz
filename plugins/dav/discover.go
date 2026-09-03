package dav

import (
	"fmt"
	"net/http"
	"net/url"

	"git.mehdix.org/alborz"
)

// SanityCheckURL asks the endpoint whether it is there at all.
func SanityCheckURL(u *url.URL) error {
	req, err := http.NewRequest(http.MethodOptions, u.String(), nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: alborz.RoundTripTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("HTTP request failed: %v %v", resp.StatusCode, resp.Status)
	}
	return nil
}
