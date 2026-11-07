package notify

import (
	"context"
	"net/http"
	"net/url"
)

type discord struct {
	id, token string
	c         *http.Client
	base      string
}

func parseDiscord(u *url.URL, c *http.Client) (Sender, error) {
	token := u.User.Username()
	if token == "" {
		return nil, invalid("discord", "token is missing")
	}
	if u.Hostname() == "" {
		return nil, invalid("discord", "webhook id is missing")
	}
	return &discord{id: u.Hostname(), token: token, c: c, base: "https://discord.com"}, nil
}

func (d *discord) Service() string { return "discord" }

func (d *discord) Send(ctx context.Context, m Message) error {
	return postJSON(ctx, d.c, "discord", d.base+"/api/webhooks/"+url.PathEscape(d.id)+"/"+url.PathEscape(d.token),
		map[string]string{"content": m.Text()})
}
