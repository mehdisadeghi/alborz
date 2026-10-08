package alborz

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// srvName is the SRV record (RFC 2782) a service is looked up at.
func srvName(service, domain string) string {
	return "_" + service + "._tcp." + domain
}

func discoverTCP(service, name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), RoundTripTimeout)
	defer cancel()
	_, addrs, err := net.DefaultResolver.LookupSRV(ctx, service, "tcp", name)
	if dnsErr, ok := err.(*net.DNSError); ok {
		if dnsErr.IsTemporary {
			return "", err
		}
	} else if err != nil {
		return "", err
	}

	if len(addrs) == 0 {
		return "", nil
	}
	addr := addrs[0]

	target := strings.TrimSuffix(addr.Target, ".")
	if target == "" {
		return "", nil
	}

	return fmt.Sprintf("%v:%v", target, addr.Port), nil
}

type discoveredService struct{ lookup, scheme string }

func discoverServices(domain string, services ...discoveredService) (*url.URL, string, error) {
	for _, service := range services {
		host, err := discoverTCP(service.lookup, domain)
		if err != nil {
			return nil, "", err
		}
		if host != "" {
			return &url.URL{Scheme: service.scheme, Host: host}, srvName(service.lookup, domain), nil
		}
	}
	return nil, "", nil
}

// discoverIMAP discovers the IMAP server via DNS (RFC 6186 section 3.2).
func discoverIMAP(domain string) (*url.URL, string, error) {
	u, source, err := discoverServices(domain,
		discoveredService{"imaps", "imaps"}, discoveredService{"imap", "imap"})
	if err != nil {
		return nil, "", err
	}
	if u != nil {
		return u, source, nil
	}

	return nil, "", fmt.Errorf("IMAP service discovery not configured for domain %q", domain)
}

// discoverSieve discovers the ManageSieve server via DNS (RFC 5804); a missing
// record is no URL and no error.
func discoverSieve(domain string) (*url.URL, string, error) {
	return discoverServices(domain, discoveredService{"sieve", "sieve"})
}

// discoverSMTP discovers the submission server via DNS (RFC 6186 section 3.1),
// including the implicit-TLS submissions service of RFC 8314; a missing record
// is no URL and no error.
func discoverSMTP(domain string) (*url.URL, string, error) {
	u, source, err := discoverServices(domain,
		discoveredService{"submissions", "smtps"}, discoveredService{"submission", "smtp"})
	if err != nil {
		return nil, "", err
	}
	return u, source, nil
}
