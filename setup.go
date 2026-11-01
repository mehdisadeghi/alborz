package alborz

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-smtp"
)

var (
	ErrSetupClosed = errors.New("initial setup is not available")
	ErrSetupCode   = errors.New("the setup code is not valid")
)

// SetupProvider names a mail domain and its services. It never holds credentials.
type SetupProvider struct {
	Domain  string
	IMAP    string
	SMTP    string
	CalDAV  string
	CardDAV string
	Sieve   string
}

func (p SetupProvider) Services() DomainConfig {
	services := DomainConfig{"imap": p.IMAP, "smtp": p.SMTP}
	for name, value := range map[string]string{"caldav": p.CalDAV, "carddav": p.CardDAV, "sieve": p.Sieve} {
		if value != "" {
			services[name] = value
		}
	}
	return services
}

// SetupSite is where readers reach alborz and the proxies in front, saved beside the domains; empty is unset.
type SetupSite struct {
	PublicURL      string
	TrustedProxies []string
}

// ParsePublicURL reads scheme://host[:port]: alborz answers at the root of its host only.
func ParsePublicURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.Trim(u.Path, "/") != "" {
		return nil, fmt.Errorf("invalid public URL %q: want scheme://host", raw)
	}
	return u, nil
}

// setupPublicURL is the origin of raw; plain http is for loopback and private hosts only.
func setupPublicURL(raw string) (string, error) {
	u, err := ParsePublicURL(raw)
	if err != nil {
		return "", &SetupError{"publicurl", err}
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" {
		if ip, err := netip.ParseAddr(u.Hostname()); err != nil || publicAddr(ip) {
			return "", &SetupError{"publicurl", errors.New("use https for a public host")}
		}
	}
	return u.Scheme + "://" + u.Host, nil
}

// SetupError identifies the field or service that needs attention.
type SetupError struct {
	Field string
	Err   error
}

func (e *SetupError) Error() string { return e.Field + ": " + e.Err.Error() }
func (e *SetupError) Unwrap() error { return e.Err }

func (s *Server) startSetup() { s.setupCode = rand.Text() }

// NeedsSetup reports an instance with neither explicit nor stored mail servers.
func (s *Server) NeedsSetup() bool {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	return s.setupCode != ""
}

// SetupCode is for the operator's console, never a public page or URL.
func (s *Server) SetupCode() string {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	return s.setupCode
}

func (s *Server) checkSetup(code string) error {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	return s.checkSetupLocked(code)
}

func (s *Server) checkSetupLocked(code string) error {
	if s.setupCode == "" {
		return ErrSetupClosed
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(s.setupCode)) != 1 {
		return ErrSetupCode
	}
	return nil
}

func setupDomain(email string) (string, error) {
	a, err := mail.ParseAddress(email)
	if err != nil || a.Address != email {
		return "", &SetupError{"email", errors.New("enter a full email address without a display name")}
	}
	_, domain, ok := strings.Cut(email, "@")
	if !ok || domain == "" || strings.ContainsAny(domain, "@:/[] ") {
		return "", &SetupError{"email", errors.New("enter a full email address")}
	}
	return domain, nil
}

func setupURL(raw, field string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, &SetupError{field, errors.New("use a server URL with only a host and optional port")}
	}
	secure := field + "s"
	if u.Scheme != field && u.Scheme != secure {
		return nil, &SetupError{field, fmt.Errorf("use %s or %s with verified TLS", field, secure)}
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, &SetupError{field, errors.New("port must be between 1 and 65535")}
		}
	}
	port := u.Port()
	if port == "" {
		ports := map[string]string{"imap": "143", "imaps": "993", "smtp": "587", "smtps": "465"}
		port = ports[u.Scheme]
	}
	u.Host = net.JoinHostPort(u.Hostname(), port)
	return u, nil
}

func validateSetup(email string, p SetupProvider) (SetupProvider, error) {
	domain, err := setupDomain(email)
	if err != nil {
		return SetupProvider{}, err
	}
	if p.Domain != "" && p.Domain != domain {
		return SetupProvider{}, &SetupError{"email", errors.New("the address does not match the discovered domain")}
	}
	imap, err := setupURL(p.IMAP, "imap")
	if err != nil {
		return SetupProvider{}, err
	}
	smtp, err := setupURL(p.SMTP, "smtp")
	if err != nil {
		return SetupProvider{}, err
	}
	p.Domain, p.IMAP, p.SMTP = domain, imap.String(), smtp.String()
	if _, err := parseDomainConfig(domain, p.Services()); err != nil {
		return SetupProvider{}, &SetupError{"additional", err}
	}
	return p, nil
}

// stageSetup keeps checked servers in memory until the owner finishes setup.
func (s *Server) stageSetup(code string, p SetupProvider) error {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if err := s.checkSetupLocked(code); err != nil {
		return err
	}
	checked, err := validateSetup("setup@"+p.Domain, p)
	if err != nil {
		return err
	}
	if s.setupProviders == nil {
		s.setupProviders = make(map[string]SetupProvider)
	}
	s.setupProviders[checked.Domain] = checked
	return nil
}

// ResetSetup clears the pending draft only for its owner.
func (s *Server) ResetSetup(code string) {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if err := s.checkSetupLocked(code); err != nil {
		return
	}
	s.setupProviders = nil
}

// SetupDomains lists pending domains only to the setup owner.
func (s *Server) SetupDomains(code string) []string {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if s.checkSetupLocked(code) != nil {
		return nil
	}
	domains := make([]string, 0, len(s.setupProviders))
	for domain := range s.setupProviders {
		domains = append(domains, domain)
	}
	slices.Sort(domains)
	return domains
}

// FinishSetup saves all checked domains and site together; a failed save leaves setup open.
// The site takes effect at the next start.
func (s *Server) FinishSetup(code string, site SetupSite) error {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if err := s.checkSetupLocked(code); err != nil {
		return err
	}
	if len(s.setupProviders) == 0 {
		return &SetupError{"email", errors.New("add a mail domain first")}
	}
	if site.PublicURL != "" {
		var err error
		if site.PublicURL, err = setupPublicURL(site.PublicURL); err != nil {
			return err
		}
	}
	config := make(map[string]DomainConfig, len(s.setupProviders))
	for domain, p := range s.setupProviders {
		config[domain] = p.Services()
	}
	if s.Options.SaveSetup == nil {
		return &SetupError{"storage", errors.New("no configuration file is available")}
	}
	if err := s.Options.SaveSetup(config, site); err != nil {
		return &SetupError{"storage", err}
	}
	domains := make(map[string]*domainUpstreams, len(s.setupProviders))
	for domain, p := range s.setupProviders {
		imap, _ := url.Parse(p.IMAP)
		smtp, _ := url.Parse(p.SMTP)
		d, _ := parseDomainConfig(domain, p.Services())
		d.imap.host, d.imap.tls = imap.Host, imap.Scheme == "imaps"
		d.smtp.host, d.smtp.tls = smtp.Host, smtp.Scheme == "smtps"
		var sieveURL *url.URL
		if p.Sieve == "" {
			var err error
			sieveURL, _, err = discoverSieve(domain)
			if err != nil {
				s.log.Printf("Domain %q: failed to discover ManageSieve server: %v", domain, err)
			}
		} else {
			sieveURL, _ = serviceURL("sieve", p.Sieve)
		}
		if sieveURL != nil {
			d.sieve.host = hostWithDefaultPort(sieveURL.Host, false, "4190", "4190")
			d.sieve.insecure = sieveURL.Scheme == "sieve+insecure"
		}
		domains[domain] = d
	}
	s.domainsMu.Lock()
	s.domains = domains
	s.domainsMu.Unlock()
	for _, ready := range s.OnSetup {
		ready()
	}
	s.setupCode, s.setupProviders = "", nil
	return nil
}

// DiscoverSetup reuses mail SRV discovery after checking the operator's setup code.
func (s *Server) DiscoverSetup(ctx context.Context, code, email string) (SetupProvider, error) {
	if err := s.checkSetup(code); err != nil {
		return SetupProvider{}, err
	}
	domain, err := setupDomain(email)
	if err != nil {
		return SetupProvider{}, err
	}
	p := SetupProvider{Domain: domain}
	ctx, cancel := context.WithTimeout(ctx, RoundTripTimeout)
	defer cancel()
	imap, _, imapErr := discoverServicesContext(ctx, domain, discoveredService{"imaps", "imaps"}, discoveredService{"imap", "imap"})
	smtp, _, smtpErr := discoverServicesContext(ctx, domain, discoveredService{"submissions", "smtps"}, discoveredService{"submission", "smtp"})
	if imap != nil {
		p.IMAP = imap.String()
	}
	if smtp != nil {
		p.SMTP = smtp.String()
	}
	if imapErr != nil || smtpErr != nil {
		return p, &SetupError{"discovery", errors.Join(imapErr, smtpErr)}
	}
	if p.IMAP == "" || p.SMTP == "" {
		return p, &SetupError{"discovery", errors.New("mail server discovery is incomplete; enter the missing servers")}
	}
	return p, nil
}

// AddSetup checks both mail services before keeping a domain for setup.
func (s *Server) AddSetup(ctx context.Context, code, email string, provider SetupProvider) error {
	if err := s.checkSetup(code); err != nil {
		return err
	}
	p, err := validateSetup(email, provider)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, signInTimeout)
	defer cancel()
	for _, service := range []struct{ field, raw string }{{"imap", p.IMAP}, {"smtp", p.SMTP}} {
		u, _ := url.Parse(service.raw)
		if err := verifySetupService(ctx, service.field, u); err != nil {
			return &SetupError{service.field, err}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.stageSetup(code, p)
}

func verifySetupService(ctx context.Context, field string, u *url.URL) error {
	conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return err
	}
	defer conn.Close()
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { rawConn.Close() })
	defer stop()
	secure := &tls.Config{ServerName: u.Hostname()}
	if strings.HasSuffix(u.Scheme, "s") {
		wrapped := tls.Client(conn, secure)
		if err := wrapped.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = wrapped
	}
	if field == "imap" {
		var c *imapclient.Client
		if u.Scheme == "imap" {
			c, err = imapclient.NewStartTLS(conn, &imapclient.Options{TLSConfig: secure})
		} else {
			c = imapclient.New(conn, nil)
		}
		if err != nil {
			return err
		}
		defer c.Close()
		return verifySetupIMAP(c)
	}
	var c *smtp.Client
	if u.Scheme == "smtp" {
		c, err = smtp.NewClientStartTLS(conn, secure)
	} else {
		c = smtp.NewClient(conn)
	}
	if err != nil {
		return err
	}
	defer c.Close()
	return verifySetupSMTP(c)
}

func verifySetupIMAP(c *imapclient.Client) error {
	if caps := c.Caps(); !caps.Has(imap.CapIMAP4rev1) && !caps.Has(imap.CapIMAP4rev2) {
		return errors.New("the server does not support IMAP4rev1 or IMAP4rev2")
	}
	return nil
}

func verifySetupSMTP(c *smtp.Client) error {
	return c.Noop()
}
