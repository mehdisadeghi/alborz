package alborz

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRefuseLocalKeepsToPublicAddresses(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:443", "10.0.0.1:443", "192.168.1.1:443",
		"172.16.0.1:443", "169.254.169.254:443", "0.0.0.0:443",
		"[::1]:443", "[fe80::1]:443", "[fd00::1]:443", "[::ffff:10.0.0.1]:443"} {
		if err := refuseLocal("tcp", addr, nil); err == nil {
			t.Errorf("%s was allowed", addr)
		}
	}
	for _, addr := range []string{"93.184.216.34:443", "[2606:2800:220:1:248:1893:25c8:1946]:443"} {
		if err := refuseLocal("tcp", addr, nil); err != nil {
			t.Errorf("%s was refused: %v", addr, err)
		}
	}
}

func TestRemoteClientSpeaksOnlyHTTPS(t *testing.T) {
	c := NewRemoteClient(time.Second)
	_, err := c.Get("http://127.0.0.1:1/")
	if err == nil || !strings.Contains(err.Error(), "not https") {
		t.Errorf("plain http was attempted: %v", err)
	}
	_, err = c.Get("https://127.0.0.1:1/")
	if err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Errorf("loopback was dialled: %v", err)
	}
	if c.CheckRedirect(&http.Request{}, make([]*http.Request, maxRemoteRedirects)) == nil {
		t.Error("a redirect chain past the cap was followed")
	}
}
