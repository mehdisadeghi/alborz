package alborz

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// maxRemoteRedirects is how far a remote server may send us on: enough
// for a CDN's hop or two, too few to be worth abusing.
const maxRemoteRedirects = 3

// NewRemoteClient makes a client for a request to somebody else's
// server on a reader's behalf: an unsubscribe endpoint, an image in a
// message. Those addresses come from the message, so they are the
// sender's to choose, and a sender must not get to choose a machine
// on our own network. Only https is spoken, at every hop, and the
// address is checked at the socket: a name that resolves to something
// else the second time it is asked cannot get around it.
func NewRemoteClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: dialTimeout, Control: refuseLocal}
	return &http.Client{
		Timeout: timeout,
		Transport: httpsOnly{&http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: dialTimeout,
		}},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRemoteRedirects {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

type httpsOnly struct{ rt http.RoundTripper }

func (t httpsOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("refusing to fetch %s: not https", req.URL)
	}
	return t.rt.RoundTrip(req)
}

// refuseLocal is the dialer's last look at where it is about to
// connect: loopback, link-local and private ranges are ours, not the
// sender's to name.
func refuseLocal(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return fmt.Errorf("refusing to connect to %s: not a public address", host)
	}
	return nil
}
