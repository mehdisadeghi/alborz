package alborz

import (
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
	_, addrs, err := net.LookupSRV(service, "tcp", name)
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

// discoverIMAP discovers the IMAP server via DNS (RFC 6186 section 3.2).
func discoverIMAP(domain string) (*url.URL, string, error) {
	imapsHost, err := discoverTCP("imaps", domain)
	if err != nil {
		return nil, "", err
	}
	if imapsHost != "" {
		return &url.URL{Scheme: "imaps", Host: imapsHost}, srvName("imaps", domain), nil
	}

	imapHost, err := discoverTCP("imap", domain)
	if err != nil {
		return nil, "", err
	}
	if imapHost != "" {
		return &url.URL{Scheme: "imap", Host: imapHost}, srvName("imap", domain), nil
	}

	return nil, "", fmt.Errorf("IMAP service discovery not configured for domain %q", domain)
}

// discoverSieve discovers the ManageSieve server via DNS (RFC 5804); a missing
// record is no URL and no error.
func discoverSieve(domain string) (*url.URL, string, error) {
	host, err := discoverTCP("sieve", domain)
	if err != nil {
		return nil, "", err
	}
	if host == "" {
		return nil, "", nil
	}
	return &url.URL{Scheme: "sieve", Host: host}, srvName("sieve", domain), nil
}

// discoverSMTP discovers the submission server via DNS (RFC 6186 section 3.1),
// including the implicit-TLS submissions service of RFC 8314.
func discoverSMTP(domain string) (*url.URL, string, error) {
	smtpsHost, err := discoverTCP("submissions", domain)
	if err != nil {
		return nil, "", err
	}
	if smtpsHost != "" {
		return &url.URL{Scheme: "smtps", Host: smtpsHost}, srvName("submissions", domain), nil
	}

	smtpHost, err := discoverTCP("submission", domain)
	if err != nil {
		return nil, "", err
	}
	if smtpHost != "" {
		return &url.URL{Scheme: "smtp", Host: smtpHost}, srvName("submission", domain), nil
	}

	return nil, "", fmt.Errorf("SMTP service discovery not configured for domain %q", domain)
}
