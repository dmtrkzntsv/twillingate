// Package notify sends one plain-text message to one channel named by a URL.
// The URL formats are shoutrrr's: telegram, ntfy, slack, discord, generic
// (a JSON webhook) and smtp. Errors never contain the URL or any part of it
// that is a secret; they name the service, the host and the status only.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Message is what a notification says. Title and Link may be empty.
type Message struct {
	Title, Body, Link string
}

// Text renders the message as one text for services that take no title:
// Title, Body and Link on their own lines, empty parts left out.
func (m Message) Text() string {
	var parts []string
	for _, p := range []string{m.Title, m.Body, m.Link} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n")
}

// Sender delivers a Message to one channel.
type Sender interface {
	Send(ctx context.Context, m Message) error
	// Service is the URL scheme name, for display ("telegram").
	Service() string
}

var (
	// ErrInvalidURL marks a URL Parse cannot use.
	ErrInvalidURL = errors.New("invalid notification url")
	// ErrPermanent marks a send that retrying will not fix: a 4xx other than
	// 408 and 429, an authentication failure, an SMTP 5xx.
	ErrPermanent = errors.New("permanent notification failure")
	// ErrTemporary marks a send worth retrying: a network error, a timeout, a
	// 5xx, 408, 429 or an SMTP 4xx.
	ErrTemporary = errors.New("temporary notification failure")
)

const timeout = 15 * time.Second

// Parse turns a notification URL into a Sender.
func Parse(raw string) (Sender, error) {
	return ParseWith(raw, &http.Client{Timeout: timeout})
}

// ParseWith is Parse with the HTTP client the HTTP-based senders use; a nil
// client means one with a 15s timeout.
func ParseWith(raw string, c *http.Client) (Sender, error) {
	if c == nil {
		c = &http.Client{Timeout: timeout}
	}
	if strings.TrimSpace(raw) == "" {
		return nil, invalid("", "url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		// url.Error carries the raw URL; say nothing of it.
		return nil, invalid("", "url is malformed")
	}
	switch u.Scheme {
	case "telegram":
		return parseTelegram(u, c)
	case "ntfy":
		return parseNtfy(u, c)
	case "slack":
		return parseSlack(u, c)
	case "discord":
		return parseDiscord(u, c)
	case "generic", "generic+http":
		return parseGeneric(u, c)
	case "smtp":
		return parseSMTP(u)
	case "":
		return nil, invalid("", "scheme is missing")
	}
	return nil, invalid("", "unknown scheme "+quoteScheme(u.Scheme))
}

func quoteScheme(s string) string {
	if len(s) > 20 {
		s = s[:20]
	}
	return fmt.Sprintf("%q", s)
}

func invalid(service, problem string) error {
	if service == "" {
		return fmt.Errorf("%w: %s", ErrInvalidURL, problem)
	}
	return fmt.Errorf("%w: %s: %s", ErrInvalidURL, service, problem)
}

// post sends body to rawURL and classifies the outcome. The error names the
// service and host, never the URL: paths and queries carry tokens.
func post(ctx context.Context, c *http.Client, service, rawURL string, header http.Header, user, pass string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: %s: bad request", ErrPermanent, service)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	host := req.URL.Host
	resp, err := c.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%w: %s: request to %s failed: %v", ErrTemporary, service, host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	kind := ErrPermanent
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests {
		kind = ErrTemporary
	}
	return fmt.Errorf("%w: %s: %s answered %d", kind, service, host, resp.StatusCode)
}

func postJSON(ctx context.Context, c *http.Client, service, rawURL string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: %s: encode body", ErrPermanent, service)
	}
	h := http.Header{"Content-Type": {"application/json"}}
	return post(ctx, c, service, rawURL, h, "", "", b)
}
