package alborz

import (
	"fmt"

	"github.com/emersion/go-smtp"
)

func (s *Server) dialSMTP(domain string) (*smtp.Client, error) {
	d, _ := s.upstreamsFor(domain)
	if d.smtp.host == "" {
		return nil, fmt.Errorf("SMTP is disabled for domain %q", domain)
	}

	var c *smtp.Client
	var err error
	kind := "SMTP"
	switch {
	case d.smtp.tls:
		kind = "SMTPS"
		c, err = smtp.DialTLS(d.smtp.host, nil)
	case !d.smtp.insecure:
		c, err = smtp.DialStartTLS(d.smtp.host, nil)
	default:
		c, err = smtp.Dial(d.smtp.host)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s server: %v", kind, err)
	}

	return c, nil
}
