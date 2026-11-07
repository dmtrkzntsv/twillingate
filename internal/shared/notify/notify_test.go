package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func withBase(s Sender, base string) {
	switch v := s.(type) {
	case *telegram:
		v.base = base
	case *ntfy:
		v.base = base
	case *slack:
		v.base = base
	case *discord:
		v.base = base
	case *generic:
		v.base = base
	}
}

type captured struct {
	method, path, query string
	header              http.Header
	body                string
}

type recorder struct {
	mu   sync.Mutex
	reqs []captured
}

func (r *recorder) all() []captured {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]captured(nil), r.reqs...)
}

func newServer(t *testing.T, status int) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, captured{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(b)})
		rec.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func mustParse(t *testing.T, raw string, srv *httptest.Server) Sender {
	t.Helper()
	var c *http.Client
	if srv != nil {
		c = srv.Client()
	}
	s, err := ParseWith(raw, c)
	if err != nil {
		t.Fatalf("ParseWith(%q): %v", raw, err)
	}
	if srv != nil {
		withBase(s, srv.URL)
	}
	return s
}

func TestParse(t *testing.T) {
	good := map[string]string{
		"telegram://tok@telegram?chats=1,2":                  "telegram",
		"ntfy://ntfy.sh/topic":                               "ntfy",
		"ntfy://u:p@host:8080/topic?scheme=http":             "ntfy",
		"slack://hook:a-b-c@webhook":                         "slack",
		"discord://tok@123":                                  "discord",
		"generic://example.com/hook?x=1":                     "generic",
		"generic+http://example.com:8080/hook":               "generic",
		"smtp://u:p@h:587/?from=a@b.test&to=c@d.test,e@f.te": "smtp",
		"smtp://h:25/?from=a@b.test&to=c@d.test&starttls=no": "smtp",
	}
	for raw, service := range good {
		s, err := Parse(raw)
		if err != nil {
			t.Errorf("Parse(%q): %v", raw, err)
			continue
		}
		if s.Service() != service {
			t.Errorf("Parse(%q).Service() = %q, want %q", raw, s.Service(), service)
		}
	}
	bad := []string{
		"",
		"foo://x",
		"telegram://@telegram?chats=1",
		"telegram://t@telegram",
		"slack://hook:a-b@webhook",
		"smtp://h:25/?from=a@b",
		"ntfy://host",
		"ntfy://host/t?scheme=ftp",
		"discord://@123",
		"generic:///path",
	}
	for _, raw := range bad {
		if _, err := Parse(raw); !errors.Is(err, ErrInvalidURL) {
			t.Errorf("Parse(%q) err = %v, want ErrInvalidURL", raw, err)
		}
	}
}

func TestText(t *testing.T) {
	cases := []struct {
		m    Message
		want string
	}{
		{Message{"t", "b", "l"}, "t\nb\nl"},
		{Message{"t", "", "l"}, "t\nl"},
		{Message{"", "b", ""}, "b"},
		{Message{}, ""},
	}
	for _, c := range cases {
		if got := c.m.Text(); got != c.want {
			t.Errorf("Text(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}

func TestTelegramPlainText(t *testing.T) {
	srv, rec := newServer(t, 200)
	s := mustParse(t, "telegram://TOK@telegram?chats=11,22", srv)
	err := s.Send(context.Background(), Message{Title: "a_b *c*", Body: "<x> & y", Link: "https://c/x"})
	if err != nil {
		t.Fatal(err)
	}
	reqs := rec.all()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}
	for i, chat := range []string{"11", "22"} {
		r := reqs[i]
		if r.method != "POST" || r.path != "/botTOK/sendMessage" {
			t.Errorf("request %d: %s %s", i, r.method, r.path)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(r.body), &got); err != nil {
			t.Fatal(err)
		}
		if got["text"] != "a_b *c*\n<x> & y\nhttps://c/x" {
			t.Errorf("text = %q", got["text"])
		}
		if _, ok := got["parse_mode"]; ok {
			t.Error("parse_mode must not be set")
		}
		if got["chat_id"] != chat || got["disable_web_page_preview"] != true {
			t.Errorf("body = %v", got)
		}
	}
}

func TestTelegramColonToken(t *testing.T) {
	srv, rec := newServer(t, 200)
	s := mustParse(t, "telegram://110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw@telegram?chats=1", srv)
	if err := s.Send(context.Background(), Message{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.all()[0].path; got != "/bot110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw/sendMessage" {
		t.Errorf("path = %q", got)
	}
	for _, raw := range []string{"telegram://123:ABC@telegram", "telegram://@telegram?chats=1"} {
		if _, err := Parse(raw); !errors.Is(err, ErrInvalidURL) {
			t.Errorf("Parse(%q) err = %v", raw, err)
		}
	}
}

func TestTelegramStopsOnCancel(t *testing.T) {
	srv, rec := newServer(t, 200)
	s := mustParse(t, "telegram://1:A@telegram?chats=1,2", srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Send(ctx, Message{Title: "x"}); !errors.Is(err, ErrTemporary) {
		t.Errorf("err = %v", err)
	}
	if n := len(rec.all()); n != 0 {
		t.Errorf("%d requests after cancel", n)
	}
}

func TestSendErrorsClassified(t *testing.T) {
	for status, want := range map[int]error{
		401: ErrPermanent, 404: ErrPermanent, 429: ErrTemporary,
		408: ErrTemporary, 503: ErrTemporary, 500: ErrTemporary,
	} {
		srv, _ := newServer(t, status)
		s := mustParse(t, "telegram://TOK@telegram?chats=1", srv)
		err := s.Send(context.Background(), Message{Title: "x"})
		if !errors.Is(err, want) {
			t.Errorf("status %d: err = %v, want %v", status, err, want)
		}
		other := ErrTemporary
		if want == ErrTemporary {
			other = ErrPermanent
		}
		if errors.Is(err, other) {
			t.Errorf("status %d: wraps both kinds", status)
		}
	}
	srv, _ := newServer(t, 200)
	s := mustParse(t, "telegram://TOK@telegram?chats=1", srv)
	srv.Close()
	if err := s.Send(context.Background(), Message{Title: "x"}); !errors.Is(err, ErrTemporary) {
		t.Errorf("closed server: err = %v, want ErrTemporary", err)
	}
}

func TestErrorsHideSecrets(t *testing.T) {
	const secret = "SECRET123"
	srv, _ := newServer(t, 401)
	s := mustParse(t, "telegram://"+secret+"@telegram?chats=1", srv)
	if err := s.Send(context.Background(), Message{Title: "x"}); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("401 error = %v", err)
	}
	srv2, _ := newServer(t, 200)
	s2 := mustParse(t, "telegram://"+secret+"@telegram?chats=1", srv2)
	srv2.Close()
	if err := s2.Send(context.Background(), Message{Title: "x"}); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("unreachable error = %v", err)
	}
	srv3, _ := newServer(t, 401)
	s4 := mustParse(t, "telegram://HALFID:HALFSECRET@telegram?chats=1", srv3)
	for _, e := range []error{
		s4.Send(context.Background(), Message{Title: "x"}),
		func() error { c, f := context.WithCancel(context.Background()); f(); return s4.Send(c, Message{}) }(),
	} {
		if e == nil || strings.Contains(e.Error(), "HALFID") || strings.Contains(e.Error(), "HALFSECRET") {
			t.Errorf("colon token error = %v", e)
		}
	}
	srv3.Close()
	if e := s4.Send(context.Background(), Message{Title: "x"}); e == nil || strings.Contains(e.Error(), "HALF") {
		t.Errorf("colon token unreachable error = %v", e)
	}
	// Default base, unreachable via a dialer that always fails.
	s3, _ := ParseWith("discord://"+secret+"@999", &http.Client{Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("boom") },
	}})
	if err := s3.Send(context.Background(), Message{Title: "x"}); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("dial error = %v", err)
	}
	for _, raw := range []string{
		"smtp://u:" + secret + "@h:25/?from=a@b",
		"slack://hook:" + secret + "-b@webhook",
		"telegram://" + secret + "@telegram",
		"telegram://x:" + secret + "@telegram",
		"discord://" + secret + "@",
	} {
		_, err := Parse(raw)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("Parse(%q) err = %v", raw, err)
		}
	}
}

func TestNtfyHeaders(t *testing.T) {
	srv, rec := newServer(t, 200)
	s := mustParse(t, "ntfy://bob:pw@ntfy.example/alerts", srv)
	if err := s.Send(context.Background(), Message{Title: "Down", Body: "site is down", Link: "https://l/x"}); err != nil {
		t.Fatal(err)
	}
	r := rec.all()[0]
	if r.method != "POST" || r.path != "/alerts" || r.body != "site is down" {
		t.Errorf("got %+v", r)
	}
	if r.header.Get("Title") != "Down" || r.header.Get("Click") != "https://l/x" {
		t.Errorf("headers %v", r.header)
	}
	if u, p, ok := (&http.Request{Header: r.header}).BasicAuth(); !ok || u != "bob" || p != "pw" {
		t.Errorf("basic auth = %q %q %v", u, p, ok)
	}
}

func TestGenericJSON(t *testing.T) {
	srv, rec := newServer(t, 204)
	s := mustParse(t, "generic://hooks.example/in/x?k=v&n=1", srv)
	if err := s.Send(context.Background(), Message{Title: "T", Body: "B", Link: "L"}); err != nil {
		t.Fatal(err)
	}
	r := rec.all()[0]
	if r.method != "POST" || r.path != "/in/x" || r.query != "k=v&n=1" {
		t.Errorf("got %+v", r)
	}
	if r.body != `{"title":"T","body":"B","link":"L"}` {
		t.Errorf("body = %s", r.body)
	}
	if r.header.Get("Content-Type") != "application/json" {
		t.Errorf("content-type %q", r.header.Get("Content-Type"))
	}
}

func TestSlackAndDiscordBodies(t *testing.T) {
	srv, rec := newServer(t, 200)
	m := Message{Title: "T", Body: "B", Link: "L"}
	if err := mustParse(t, "slack://hook:aa-bb-cc@webhook", srv).Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if err := mustParse(t, "discord://tok@123", srv).Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	reqs := rec.all()
	if reqs[0].path != "/services/aa/bb/cc" || reqs[0].body != `{"text":"T\nB\nL"}` {
		t.Errorf("slack %+v", reqs[0])
	}
	if reqs[1].path != "/api/webhooks/123/tok" || reqs[1].body != `{"content":"T\nB\nL"}` {
		t.Errorf("discord %+v", reqs[1])
	}
}

type smtpResult struct {
	from  string
	rcpts []string
	data  string
}

func fakeSMTP(t *testing.T) (string, <-chan smtpResult) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	out := make(chan smtpResult, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		br := bufio.NewReader(c)
		say := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
		say("220 fake ESMTP")
		var res smtpResult
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			up := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
				say("250 fake")
			case strings.HasPrefix(up, "MAIL FROM:"):
				res.from = line[len("MAIL FROM:"):]
				say("250 ok")
			case strings.HasPrefix(up, "RCPT TO:"):
				res.rcpts = append(res.rcpts, line[len("RCPT TO:"):])
				say("250 ok")
			case up == "DATA":
				say("354 go")
				var sb strings.Builder
				for {
					l, err := br.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					sb.WriteString(l)
				}
				res.data = sb.String()
				say("250 queued")
			case up == "QUIT":
				say("221 bye")
				out <- res
				return
			default:
				say("250 ok")
			}
		}
	}()
	return ln.Addr().String(), out
}

func TestSMTP(t *testing.T) {
	addr, out := fakeSMTP(t)
	s, err := Parse("smtp://" + addr + "/?from=twg@example.test&to=a@x.test,b@y.test&starttls=no")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), Message{Title: "Site down", Body: "It is down", Link: "https://l/x"}); err != nil {
		t.Fatal(err)
	}
	res := <-out
	if !strings.Contains(res.from, "twg@example.test") {
		t.Errorf("from = %q", res.from)
	}
	if len(res.rcpts) != 2 || !strings.Contains(res.rcpts[0], "a@x.test") || !strings.Contains(res.rcpts[1], "b@y.test") {
		t.Errorf("rcpts = %v", res.rcpts)
	}
	if !strings.Contains(res.data, "Subject: Site down\r\n") {
		t.Errorf("data = %q", res.data)
	}
	if !strings.Contains(res.data, "It is down\r\n\r\nhttps://l/x") {
		t.Errorf("body missing: %q", res.data)
	}
}

func TestSMTPRequiresSTARTTLS(t *testing.T) {
	addr, _ := fakeSMTP(t)
	s, err := Parse("smtp://" + addr + "/?from=a@b.test&to=c@d.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), Message{Title: "x"}); !errors.Is(err, ErrPermanent) {
		t.Errorf("err = %v, want ErrPermanent", err)
	}
}
