package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

type smtpSender struct {
	host, port string
	user, pass string
	from       string
	to         []string
	starttls   bool
}

func parseSMTP(u *url.URL) (Sender, error) {
	if u.Hostname() == "" {
		return nil, invalid("smtp", "host is missing")
	}
	port := u.Port()
	if port == "" {
		port = "587"
	}
	q := u.Query()
	from, err := mail.ParseAddress(q.Get("from"))
	if err != nil {
		return nil, invalid("smtp", "from is missing or not an address")
	}
	var to []string
	for _, t := range strings.Split(q.Get("to"), ",") {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		a, err := mail.ParseAddress(t)
		if err != nil {
			return nil, invalid("smtp", "to holds something that is not an address")
		}
		to = append(to, a.Address)
	}
	if len(to) == 0 {
		return nil, invalid("smtp", "to is missing")
	}
	s := &smtpSender{host: u.Hostname(), port: port, from: from.Address, to: to, starttls: q.Get("starttls") != "no"}
	s.user = u.User.Username()
	s.pass, _ = u.User.Password()
	return s, nil
}

func (s *smtpSender) Service() string { return "smtp" }

func (s *smtpSender) Send(ctx context.Context, m Message) error {
	err := s.send(ctx, m)
	if err == nil {
		return nil
	}
	// Server text and library errors can echo the user or the address; keep
	// the class and the SMTP code only.
	var te *textproto.Error
	switch {
	case errors.As(err, &te):
		kind := ErrPermanent
		if te.Code >= 400 && te.Code < 500 {
			kind = ErrTemporary
		}
		return fmt.Errorf("%w: smtp: %s answered %d", kind, s.host, te.Code)
	case errors.Is(err, errNoSTARTTLS):
		return fmt.Errorf("%w: smtp: %s does not offer STARTTLS (add starttls=no to allow plain text)", ErrPermanent, s.host)
	case errors.Is(err, errAuth):
		return fmt.Errorf("%w: smtp: authentication with %s failed", ErrPermanent, s.host)
	}
	return fmt.Errorf("%w: smtp: %s failed: %s", ErrTemporary, s.host, netReason(err))
}

var (
	errNoSTARTTLS = errors.New("no starttls")
	errAuth       = errors.New("auth")
)

// netReason is the innermost error text of a network failure.
func netReason(err error) string {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "connection error"
}

func (s *smtpSender) send(ctx context.Context, m Message) error {
	addr := net.JoinHostPort(s.host, s.port)
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(2 * timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	cfg := &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}
	if s.port == "465" {
		conn = tls.Client(conn, cfg)
	}
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if s.port != "465" && s.starttls {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errNoSTARTTLS
		}
		if err := c.StartTLS(cfg); err != nil {
			return err
		}
	}
	if s.user != "" {
		if err := c.Auth(smtp.PlainAuth("", s.user, s.pass, s.host)); err != nil {
			var te *textproto.Error
			if errors.As(err, &te) {
				return err
			}
			return errAuth
		}
	}
	if err := c.Mail(s.from); err != nil {
		return err
	}
	for _, t := range s.to {
		if err := c.Rcpt(t); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(s.message(m)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func (s *smtpSender) message(m Message) []byte {
	subject := mime.QEncoding.Encode("utf-8", oneLine(m.Title))
	body := m.Body
	if m.Link != "" {
		body += "\n\n" + m.Link
	}
	var b strings.Builder
	b.WriteString("From: " + s.from + "\r\n")
	b.WriteString("To: " + strings.Join(s.to, ", ") + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	b.WriteString("\r\n")
	return []byte(b.String())
}
