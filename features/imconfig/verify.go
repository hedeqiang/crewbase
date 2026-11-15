package imconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Check is a credentials check before (or without) saving: it asks the
// platform who the bot is. Nothing is stored and no message is sent.
type Check struct {
	Platform string `json:"platform"`
	Region   string `json:"region"`
	AppID    string `json:"appId"`
	BotToken string `json:"botToken"`
	AppToken string `json:"appToken"`
	// ID of a saved gateway whose stored tokens fill in empty ones (edit).
	ID string `json:"id"`
}

// CheckResult says whether the platform accepted the credentials.
type CheckResult struct {
	OK    bool   `json:"ok"`
	Bot   string `json:"bot,omitempty"` // the bot's name there
	Error string `json:"error,omitempty"`
}

// API roots of the platforms without a region (tests override them).
var (
	slackAPI   = "https://slack.com/api/"
	discordAPI = "https://discord.com/api/v10/"
)

var checkClient = &http.Client{Timeout: 10 * time.Second}

// Verify checks credentials against the platform. A rejected credential
// is a result (OK false), not an error; tokens never appear in it.
func (s *Service) Verify(ctx context.Context, in Check) (CheckResult, error) {
	in.Platform, in.Region = legacy(in.Platform, in.Region)
	if in.ID != "" {
		s.mu.Lock()
		g, ok := s.items[in.ID]
		s.mu.Unlock()
		// a saved gateway's tokens only go to its own platform
		if ok && (in.Platform == "" || in.Platform == g.Platform) {
			if in.Platform == "" {
				in.Platform = g.Platform
			}
			if in.Region == "" {
				in.Region = g.Region
			}
			if in.AppID == "" {
				in.AppID = g.AppID
			}
			if in.BotToken == "" {
				in.BotToken = s.resolve(botRef(g.ID))
			}
			if in.AppToken == "" {
				in.AppToken = s.resolve(appRef(g.ID))
			}
		}
	}
	p, ok := platform(in.Platform)
	if !ok {
		return CheckResult{}, fmt.Errorf("unknown platform %q", in.Platform)
	}
	r, ok := p.region(in.Region)
	if !ok {
		return CheckResult{}, fmt.Errorf("unknown region %q", in.Region)
	}
	bot, app := strings.TrimSpace(in.BotToken), strings.TrimSpace(in.AppToken)
	switch {
	case bot == "":
		return CheckResult{}, errors.New("a bot token is required")
	case p.App && app == "":
		return CheckResult{}, errors.New("an app-level token is required")
	case p.AppID && strings.TrimSpace(in.AppID) == "":
		return CheckResult{}, errors.New("an app id is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var name string
	var err error
	switch p.ID {
	case "telegram":
		name, err = s.checkTelegram(ctx, bot)
	case "slack":
		name, err = checkSlack(ctx, bot, app)
	case "discord":
		name, err = checkDiscord(ctx, bot)
	case "feishu":
		name, err = checkFeishu(ctx, r.Domain, strings.TrimSpace(in.AppID), bot)
	default:
		return CheckResult{}, fmt.Errorf("no check for %q", p.ID)
	}
	if err != nil {
		msg := err.Error()
		for _, tok := range []string{bot, app} {
			if tok != "" {
				msg = strings.ReplaceAll(msg, tok, "***")
			}
		}
		return CheckResult{Error: msg}, nil
	}
	return CheckResult{OK: true, Bot: name}, nil
}

func (s *Service) resolve(ref string) string {
	cs, err := s.creds()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, _ := cs.Resolve(ctx, ref)
	return v
}

// call sends a JSON request and decodes a JSON answer (any status).
func call(ctx context.Context, method, addr string, header map[string]string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, addr, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := checkClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL (it may carry the token)
		}
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(b, out); err != nil {
		return resp.StatusCode, fmt.Errorf("unexpected answer (HTTP %d)", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func (s *Service) checkTelegram(ctx context.Context, tok string) (string, error) {
	base := "https://api.telegram.org"
	if v, ok := s.extra["apiBase"].(string); ok && v != "" {
		base = v
	}
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
		} `json:"result"`
	}
	if _, err := call(ctx, "GET", strings.TrimRight(base, "/")+"/bot"+tok+"/getMe", nil, nil, &r); err != nil {
		return "", err
	}
	if !r.OK {
		return "", errors.New(nonEmpty(r.Description, "rejected"))
	}
	if r.Result.Username != "" {
		return "@" + r.Result.Username, nil
	}
	return r.Result.FirstName, nil
}

func checkSlack(ctx context.Context, bot, app string) (string, error) {
	var a struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		User  string `json:"user"`
		Team  string `json:"team"`
	}
	if _, err := call(ctx, "POST", slackAPI+"auth.test", map[string]string{"Authorization": "Bearer " + bot}, map[string]any{}, &a); err != nil {
		return "", err
	}
	if !a.OK {
		return "", fmt.Errorf("bot token: %s", nonEmpty(a.Error, "rejected"))
	}
	// the app-level token opens Socket Mode; asking for a URL is harmless
	var c struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if _, err := call(ctx, "POST", slackAPI+"apps.connections.open", map[string]string{"Authorization": "Bearer " + app}, map[string]any{}, &c); err != nil {
		return "", err
	}
	if !c.OK {
		return "", fmt.Errorf("app-level token: %s", nonEmpty(c.Error, "rejected"))
	}
	if a.Team != "" {
		return a.User + " · " + a.Team, nil
	}
	return a.User, nil
}

func checkDiscord(ctx context.Context, tok string) (string, error) {
	var r struct {
		Username string `json:"username"`
		Message  string `json:"message"`
	}
	code, err := call(ctx, "GET", discordAPI+"users/@me", map[string]string{"Authorization": "Bot " + tok}, nil, &r)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("HTTP %d: %s", code, nonEmpty(r.Message, "rejected"))
	}
	return r.Username, nil
}

// checkFeishu gets a tenant token for the app, then the bot's name.
func checkFeishu(ctx context.Context, domain, appID, secret string) (string, error) {
	var t struct {
		Code  int    `json:"code"`
		Msg   string `json:"msg"`
		Token string `json:"tenant_access_token"`
	}
	if _, err := call(ctx, "POST", strings.TrimRight(domain, "/")+"/open-apis/auth/v3/tenant_access_token/internal", nil,
		map[string]string{"app_id": appID, "app_secret": secret}, &t); err != nil {
		return "", err
	}
	if t.Code != 0 || t.Token == "" {
		return "", fmt.Errorf("%d: %s", t.Code, nonEmpty(t.Msg, "rejected"))
	}
	var b struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			AppName string `json:"app_name"`
		} `json:"bot"`
	}
	if _, err := call(ctx, "GET", strings.TrimRight(domain, "/")+"/open-apis/bot/v3/info", map[string]string{"Authorization": "Bearer " + t.Token}, nil, &b); err != nil {
		return "", err
	}
	if b.Code != 0 {
		// the app is valid but has no bot yet: enable the bot capability
		return "", fmt.Errorf("bot: %d: %s", b.Code, nonEmpty(b.Msg, "not enabled"))
	}
	return b.Bot.AppName, nil
}

func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
