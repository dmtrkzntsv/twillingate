package notify

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

type slack struct {
	path string // a/b/c
	c    *http.Client
	base string
}

func parseSlack(u *url.URL, c *http.Client) (Sender, error) {
	pass, _ := u.User.Password()
	if u.User.Username() != "hook" || u.Host != "webhook" {
		return nil, invalid("slack", "expected slack://hook:<a>-<b>-<c>@webhook")
	}
	parts := strings.Split(pass, "-")
	if len(parts) != 3 {
		return nil, invalid("slack", "webhook token must have three parts")
	}
	for _, p := range parts {
		if p == "" {
			return nil, invalid("slack", "webhook token has an empty part")
		}
	}
	return &slack{path: strings.Join(parts, "/"), c: c, base: "https://hooks.slack.com"}, nil
}

func (s *slack) Service() string { return "slack" }

func (s *slack) Send(ctx context.Context, m Message) error {
	return postJSON(ctx, s.c, "slack", s.base+"/services/"+s.path, map[string]string{"text": m.Text()})
}
