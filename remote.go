package alborz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

// maxRemoteRedirects is how far a remote server may send us on: enough
// for a CDN's hop or two, too few to be worth abusing.
const maxRemoteRedirects = 3

// Half the per-host capacity is kept for requests someone waits on.
const (
	connectionsPerHost = 4
	backgroundPerHost  = 2
)

// Outgoing is every request alborz sends over HTTP. Trusted dials the
// servers the deployment names or finds; Guarded dials a server
// somebody outside the deployment names, and only at a public address,
// checked at the socket, so neither a redirect nor a name that resolves
// anew gets further than the first address did. Both share one limit
// per host.
type Outgoing struct {
	Trusted, Guarded http.RoundTripper
}

func NewOutgoing() Outgoing {
	limits := &hostLimits{hosts: make(map[string]*hostLimit)}
	dialer := &net.Dialer{Timeout: dialTimeout, Control: refuseLocal}
	guarded := &http.Transport{
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: dialTimeout,
	}
	return Outgoing{
		Trusted: limitedTransport{next: http.DefaultTransport, limits: limits},
		Guarded: limitedTransport{next: guarded, limits: limits},
	}
}

// Remote makes a client for a request to somebody else's server on a
// reader's behalf: an unsubscribe endpoint, an image in a message.
// Those addresses are the sender's to choose, and a sender must not get
// to choose a machine on our own network, nor plain http at any hop.
func (o Outgoing) Remote(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: httpsOnly{o.Guarded},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRemoteRedirects {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

type backgroundKey struct{}

// Background marks work nobody is waiting on, which the per-host limit
// holds to its background share.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

type hostLimit struct{ all, background chan struct{} }

type hostLimits struct {
	mu    sync.Mutex
	hosts map[string]*hostLimit
}

func (p *hostLimits) host(name string) *hostLimit {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.hosts[name]
	if h == nil {
		h = &hostLimit{make(chan struct{}, connectionsPerHost), make(chan struct{}, backgroundPerHost)}
		p.hosts[name] = h
	}
	return h
}

// limitedTransport goes below whatever decides the host a request
// leaves for: above it, every server behind one routed address would
// share that address's share.
type limitedTransport struct {
	next   http.RoundTripper
	limits *hostLimits
}

func (t limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	h := t.limits.host(req.URL.Scheme + "://" + req.URL.Host)
	slots := []chan struct{}{h.all}
	if background, _ := req.Context().Value(backgroundKey{}).(bool); background {
		slots = []chan struct{}{h.background, h.all}
	}
	var held []chan struct{}
	release := func() {
		for _, slot := range held {
			<-slot
		}
	}
	for _, slot := range slots {
		select {
		case slot <- struct{}{}:
			held = append(held, slot)
		case <-req.Context().Done():
			release()
			return nil, req.Context().Err()
		}
	}
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		release()
		return nil, err
	}
	resp.Body = limitedBody{resp.Body, sync.OnceFunc(release)}
	return resp, nil
}

type limitedBody struct {
	io.ReadCloser
	release func()
}

func (b limitedBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
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
