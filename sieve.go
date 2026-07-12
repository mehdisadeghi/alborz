package alps

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"

	"go.guido-berhoerster.org/managesieve"
)

// SieveScript is a script stored on the ManageSieve server.
type SieveScript struct {
	Name   string
	Active bool
}

// SieveClient is the subset of the ManageSieve protocol used by alps. It
// hides the underlying library so that it can be replaced.
type SieveClient interface {
	ListScripts() ([]SieveScript, error)
	GetScript(name string) (string, error)
	// PutScript stores or replaces a script and returns server warnings.
	// The server validates the script and rejects invalid ones.
	PutScript(name, content string) (warnings string, err error)
	// ActivateScript makes the named script the only active one. An
	// empty name deactivates all scripts.
	ActivateScript(name string) error
	DeleteScript(name string) error
	Logout() error
	Close() error
}

// DialSieveFunc connects to the upstream ManageSieve server and
// authenticates with the given credentials.
type DialSieveFunc func(username, password string) (SieveClient, error)

func (s *Server) parseSieveUpstream() error {
	// Unlike Server.Upstream, the scheme-less bare domain upstream must
	// not win over an explicit sieve:// URL, so look up schemes directly.
	var u *url.URL
	for _, scheme := range []string{"sieve", "sieve+insecure"} {
		v, ok := s.upstreams[scheme]
		if !ok {
			continue
		}
		if u != nil {
			return fmt.Errorf("multiple upstream ManageSieve servers configured")
		}
		u = v
	}
	if u == nil {
		v, ok := s.upstreams[""]
		if !ok {
			return nil
		}
		var err error
		u, err = discoverSieve(v.Host)
		if err != nil {
			s.e.Logger.Printf("Failed to discover ManageSieve server: %v", err)
			return nil
		}
		if u == nil {
			// sieve is optional
			return nil
		}
	}

	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(host, "4190")
	}

	s.sieve.host = host
	s.sieve.insecure = u.Scheme == "sieve+insecure"

	s.e.Logger.Printf("Configured upstream ManageSieve server: %v", host)
	return nil
}

// SieveEnabled reports whether an upstream ManageSieve server is configured.
func (s *Server) SieveEnabled() bool {
	return s.sieve.host != ""
}

func (s *Server) dialSieve(username, password string) (SieveClient, error) {
	if s.sieve.host == "" {
		return nil, fmt.Errorf("ManageSieve is disabled")
	}

	c, err := managesieve.Dial(s.sieve.host)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to ManageSieve server: %v", err)
	}

	host, _, _ := net.SplitHostPort(s.sieve.host)
	if !s.sieve.insecure {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			c.Close()
			return nil, fmt.Errorf("STARTTLS failed: %v", err)
		}
	}

	if err := c.Authenticate(managesieve.PlainAuth("", username, password, host)); err != nil {
		c.Close()
		return nil, AuthError{err}
	}

	return &sieveClient{c}, nil
}

// sieveClient adapts go.guido-berhoerster.org/managesieve to SieveClient.
type sieveClient struct {
	c *managesieve.Client
}

func (sc *sieveClient) ListScripts() ([]SieveScript, error) {
	names, active, err := sc.c.ListScripts()
	if err != nil {
		return nil, err
	}
	scripts := make([]SieveScript, len(names))
	for i, name := range names {
		scripts[i] = SieveScript{Name: name, Active: name == active}
	}
	return scripts, nil
}

func (sc *sieveClient) GetScript(name string) (string, error) {
	return sc.c.GetScript(name)
}

func (sc *sieveClient) PutScript(name, content string) (string, error) {
	return sc.c.PutScript(name, content)
}

func (sc *sieveClient) ActivateScript(name string) error {
	return sc.c.ActivateScript(name)
}

func (sc *sieveClient) DeleteScript(name string) error {
	return sc.c.DeleteScript(name)
}

func (sc *sieveClient) Logout() error {
	return sc.c.Logout()
}

func (sc *sieveClient) Close() error {
	return sc.c.Close()
}
