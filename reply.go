package main

// SMTPOutConfig holds configuration for outbound SMTP (reply-to-email feature).
type SMTPOutConfig struct {
	Host     string
	Port     int
	Username string
	Password string
}

func (c *SMTPOutConfig) IsConfigured() bool {
	return c.Host != ""
}
