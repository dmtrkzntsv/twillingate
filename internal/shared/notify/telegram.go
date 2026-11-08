package notify

import (
	"context"
	"fmt"
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
	// A bot token is <id>:<secret>; url.User splits it at the colon.
	if pass, ok := u.User.Password(); ok {
		token += ":" + pass
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

// Send posts the text to every chat. A partial failure is reported as
// success: once one chat has accepted, a retry would send it there again, so
// the notification counts as sent. Only when every chat failed is the first
// failure returned.
func (t *telegram) Send(ctx context.Context, m Message) error {
	var first error
	sent := false
	for _, chat := range t.chats {
		if ctx.Err() != nil {
			if first == nil {
				first = postJSONCtxErr(ctx)
			}
			break
		}
		err := postJSON(ctx, t.c, "telegram", t.base+"/bot"+t.token+"/sendMessage", map[string]any{
			"chat_id":                  chat,
			"text":                     m.Text(),
			"disable_web_page_preview": true,
		})
		if err == nil {
			sent = true
		} else if first == nil {
			first = err
		}
	}
	if sent {
		return nil
	}
	return first
}

func postJSONCtxErr(ctx context.Context) error {
	return fmt.Errorf("%w: telegram: %v", ErrTemporary, ctx.Err())
}
