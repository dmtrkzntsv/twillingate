package notify

import (
	"context"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

type ntfy struct {
	scheme, host, topic string
	user, pass          string
	c                   *http.Client
	base                string // scheme://host; tests point it at a server
}

func parseNtfy(u *url.URL, c *http.Client) (Sender, error) {
	if u.Host == "" {
		return nil, invalid("ntfy", "host is missing")
	}
	topic := strings.Trim(u.Path, "/")
	if topic == "" {
		return nil, invalid("ntfy", "topic is missing")
	}
	scheme := u.Query().Get("scheme")
	switch scheme {
	case "":
		scheme = "https"
	case "http", "https":
	default:
		return nil, invalid("ntfy", "scheme must be http or https")
	}
	n := &ntfy{scheme: scheme, host: u.Host, topic: topic, c: c, base: scheme + "://" + u.Host}
	n.user = u.User.Username()
	n.pass, _ = u.User.Password()
	return n, nil
}

func (n *ntfy) Service() string { return "ntfy" }

func (n *ntfy) Send(ctx context.Context, m Message) error {
	h := http.Header{}
	if m.Title != "" {
		h.Set("Title", mime.QEncoding.Encode("utf-8", oneLine(m.Title)))
	}
	if m.Link != "" {
		h.Set("Click", oneLine(m.Link))
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	return post(ctx, n.c, "ntfy", n.base+"/"+url.PathEscape(n.topic), h, n.user, n.pass, []byte(m.Body))
}

// oneLine drops CR and LF so a value cannot split a header.
func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
