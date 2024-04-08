package alps

import (
	"fmt"

	"github.com/emersion/go-smtp"
)

func (s *Server) dialSMTP() (*smtp.Client, error) {
	if s.smtp.host == "" {
		return nil, fmt.Errorf("SMTP is disabled")
	}

	var c *smtp.Client
	var err error
	if s.smtp.tls {
		c, err = smtp.DialTLS(s.smtp.host, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to SMTPS server: %v", err)
		}
	} else if !s.smtp.insecure {
		c, err = smtp.DialStartTLS(s.smtp.host, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to SMTP server: %v", err)
		}
	} else {
		c, err = smtp.Dial(s.smtp.host)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to SMTP server: %v", err)
		}
	}

	return c, err
}
