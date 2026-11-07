package notify

import (
	"context"
	"net/http"
	"net/url"
)

type generic struct {
	path, rawQuery string
	c              *http.Client
	base           string // scheme://host[:port]
}

func parseGeneric(u *url.URL, c *http.Client) (Sender, error) {
	if u.Host == "" {
		return nil, invalid("generic", "host is missing")
	}
	scheme := "https"
	if u.Scheme == "generic+http" {
		scheme = "http"
	}
	return &generic{path: u.EscapedPath(), rawQuery: u.RawQuery, c: c, base: scheme + "://" + u.Host}, nil
}

func (g *generic) Service() string { return "generic" }

func (g *generic) Send(ctx context.Context, m Message) error {
	target := g.base + g.path
	if g.rawQuery != "" {
		target += "?" + g.rawQuery
	}
	return postJSON(ctx, g.c, "generic", target, struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Link  string `json:"link"`
	}{m.Title, m.Body, m.Link})
}
