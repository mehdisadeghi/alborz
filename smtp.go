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
	if d.smtp.tls {
		c, err = smtp.DialTLS(d.smtp.host, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to SMTPS server: %v", err)
		}
	} else if !d.smtp.insecure {
		c, err = smtp.DialStartTLS(d.smtp.host, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to SMTP server: %v", err)
		}
	} else {
		c, err = smtp.Dial(d.smtp.host)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to SMTP server: %v", err)
		}
	}

	return c, err
}
