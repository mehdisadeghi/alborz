package alborz

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// DomainConfig names a domain's services. Missing services are discovered; an empty URL disables one.
type DomainConfig map[string]string

var serviceSchemes = map[string][]string{
	"imap":     {"imap", "imaps", "imap+insecure"},
	"smtp":     {"smtp", "smtps", "smtp+insecure"},
	"sieve":    {"sieve", "sieve+insecure"},
	"caldav":   {"https", "http", "http+insecure", "caldavs", "caldav+insecure"},
	"carddav":  {"https", "http", "http+insecure", "carddavs", "carddav+insecure"},
	"poppassd": {"poppassd", "poppassd+unix"},
}

func serviceURL(service, raw string) (*url.URL, error) {
	schemes, ok := serviceSchemes[service]
	if !ok {
		return nil, fmt.Errorf("unknown service %q", service)
	}
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || !slices.Contains(schemes, u.Scheme) || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("%s: expected a server URL using %s, without credentials or a fragment", service, strings.Join(schemes, ", "))
	}
	if u.Scheme == "poppassd+unix" {
		if u.Host != "" || !strings.HasPrefix(u.Path, "/") {
			return nil, fmt.Errorf("poppassd: expected an absolute Unix socket path")
		}
	} else if u.Hostname() == "" || strings.ContainsAny(u.Hostname(), " \t\r\n") {
		return nil, fmt.Errorf("%s: server URL has no valid host", service)
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("%s: port must be between 1 and 65535", service)
		}
	}
	if service != "caldav" && service != "carddav" && service != "poppassd" && (u.Path != "" || u.RawQuery != "" || u.ForceQuery) {
		return nil, fmt.Errorf("%s: server URL must contain only a host and optional port", service)
	}
	return u, nil
}

func parseDomainConfig(domain string, config DomainConfig) (*domainUpstreams, error) {
	if strings.ContainsAny(domain, ":/@?#[]\\ \t\r\n") {
		return nil, fmt.Errorf("invalid mail domain %q", domain)
	}
	d := &domainUpstreams{services: make(map[string]*url.URL)}
	for service := range serviceSchemes {
		if domain != "" && service != "poppassd" {
			d.services[service] = &url.URL{Host: domain}
		}
	}
	for service, raw := range config {
		u, err := serviceURL(service, raw)
		if err != nil {
			return nil, fmt.Errorf("domain %q: %w", domain, err)
		}
		d.services[service] = u
	}
	return d, nil
}

// ServiceDisabled reports an explicitly disabled service.
func (s *Server) ServiceDisabled(domain, service string) bool {
	d, ok := s.upstreamsFor(domain)
	if !ok {
		return false
	}
	u, set := d.services[service]
	return set && u == nil
}

// ParseUpstreams applies command-line endpoints to the selected domains or provider.
func (options *Options) ParseUpstreams(args []string) error {
	if len(args) == 0 {
		return nil
	}
	selected := make(map[string]DomainConfig)
	seen := make(map[string]bool)
	for _, arg := range args {
		domain, endpoint, explicit := strings.Cut(arg, "=")
		if strings.Contains(domain, "://") {
			domain, endpoint = "", arg
		} else if domain == "" || strings.ContainsAny(domain, ":/") {
			return errors.New("invalid upstream: expected a domain, domain=URL, or URL")
		}
		services, exists := selected[domain]
		if !exists {
			services = maps.Clone(options.Domains[domain])
			if domain == "" {
				services = maps.Clone(options.Provider)
			}
			if services == nil {
				services = DomainConfig{}
			}
			selected[domain] = services
		}
		if endpoint == "" && !explicit {
			continue
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme == "" {
			return fmt.Errorf("domain %q: invalid upstream URL", domain)
		}
		scheme, _, _ := strings.Cut(u.Scheme, "+")
		service := strings.TrimSuffix(scheme, "s")
		fields := []string{service}
		if service == "http" {
			fields = []string{"caldav", "carddav"}
		}
		for _, field := range fields {
			if _, ok := serviceSchemes[field]; !ok {
				return fmt.Errorf("unsupported upstream scheme %q", u.Scheme)
			}
			key := domain + "\x00" + field
			if seen[key] {
				return fmt.Errorf("domain %q: multiple %s upstreams", domain, field)
			}
			seen[key] = true
			services[field] = endpoint
		}
	}
	if provider, exists := selected[""]; exists {
		if len(selected) != 1 {
			return errors.New("cannot mix provider-wide servers with domain-specific servers")
		}
		options.Domains, options.Provider = nil, provider
	} else {
		options.Domains, options.Provider = selected, nil
	}
	return nil
}
