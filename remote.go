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
	return &http.Client{
		Timeout:   timeout,
		Transport: httpsOnly{NewRemoteTransport()},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRemoteRedirects {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

// NewRemoteTransport connects to public addresses only, checked at the
// socket, for any client whose host somebody outside the deployment
// names: a redirect, or a name that resolves anew, gets no further
// than the first address did.
func NewRemoteTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: dialTimeout, Control: refuseLocal}
	return &http.Transport{
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: dialTimeout,
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
	if !publicAddr(ip) {
		return fmt.Errorf("refusing to connect to %s: not a public address", host)
	}
	return nil
}

// sharedAddressSpace is a carrier's NAT (RFC 6598): as private as the
// ranges of RFC 1918, which are all netip's IsPrivate knows.
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// publicAddr says whether an address is a machine out on the internet
// rather than one of ours: not loopback, link-local, unspecified,
// private or behind a carrier's NAT.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedAddressSpace.Contains(ip)
}
