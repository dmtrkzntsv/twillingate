package notify

import (
	"context"
	"net/http"
	"net/url"
	"strings"
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

// discordEscaper backslash-escapes Discord's Markdown control characters.
var discordEscaper = strings.NewReplacer(
	`\`, `\\`, "*", `\*`, "_", `\_`, "~", `\~`, "`", "\\`", "|", `\|`, ">", `\>`,
)

// Send posts the text verbatim: Markdown escaped in the title and body (the
// link is left as is, so it stays a working link) and no mentions parsed, so
// @everyone or <@id> in a form field pings nobody.
func (d *discord) Send(ctx context.Context, m Message) error {
	esc := Message{Title: discordEscaper.Replace(m.Title), Body: discordEscaper.Replace(m.Body), Link: m.Link}
	type mentions struct {
		Parse []string `json:"parse"`
	}
	return postJSON(ctx, d.c, "discord", d.base+"/api/webhooks/"+url.PathEscape(d.id)+"/"+url.PathEscape(d.token),
		struct {
			Content         string   `json:"content"`
			AllowedMentions mentions `json:"allowed_mentions"`
		}{esc.Text(), mentions{Parse: []string{}}})
}
