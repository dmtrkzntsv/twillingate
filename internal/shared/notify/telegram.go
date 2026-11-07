package notify

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

type telegram struct {
	token string
	chats []string
	c     *http.Client
	base  string // API origin; tests point it at a server
}

func parseTelegram(u *url.URL, c *http.Client) (Sender, error) {
	token := u.User.Username()
	if token == "" {
		return nil, invalid("telegram", "token is missing")
	}
	var chats []string
	for _, ch := range strings.Split(u.Query().Get("chats"), ",") {
		if ch = strings.TrimSpace(ch); ch != "" {
			chats = append(chats, ch)
		}
	}
	if len(chats) == 0 {
		return nil, invalid("telegram", "chats is missing")
	}
	return &telegram{token: token, chats: chats, c: c, base: "https://api.telegram.org"}, nil
}

func (t *telegram) Service() string { return "telegram" }

// Send posts the text to every chat and returns the first failure, after
// trying them all.
func (t *telegram) Send(ctx context.Context, m Message) error {
	var first error
	for _, chat := range t.chats {
		err := postJSON(ctx, t.c, "telegram", t.base+"/bot"+t.token+"/sendMessage", map[string]any{
			"chat_id":                  chat,
			"text":                     m.Text(),
			"disable_web_page_preview": true,
		})
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}
