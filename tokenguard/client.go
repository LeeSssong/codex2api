// Adapted from Sub2API (LGPL-3.0); see SOURCE.md.
package tokenguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ProbeResult struct {
	State, Detail string
	LatencyMS     int
}
type Client struct{ http *http.Client }

func NewClient(transport http.RoundTripper) *Client {
	return &Client{http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) request(ctx context.Context, endpoint, kind string, headers map[string]string, payload any, limit int64) (map[string]any, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, errors.New("请求构造失败")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, errors.New("请求构造失败")
	}
	applyGuardRequestHeaders(req, endpoint, kind, headers)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, errors.New("外部服务请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, errors.New("外部服务拒绝请求")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, resp.StatusCode, errors.New("外部服务响应读取失败或超过长度限制")
	}
	result, err := parseGuardNDJSONResult(raw)
	if err != nil {
		return nil, resp.StatusCode, errors.New("外部服务响应缺少result事件")
	}
	return result, resp.StatusCode, nil
}
func (c *Client) Probe(ctx context.Context, cfg Config, token string) ProbeResult {
	if strings.TrimSpace(token) == "" {
		return ProbeResult{State: "transient", Detail: "账号没有access_token，无法确认令牌状态"}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.ProbeTimeoutSeconds)*time.Second)
	defer cancel()
	start := time.Now()
	result, status, err := c.request(ctx, cfg.ProbeEndpoint, "probe", cfg.ProbeHeaders, map[string]any{"access_token": token, "model": cfg.ProbeModel}, 2<<20)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		detail := "探活请求失败或响应无效"
		if status != 0 {
			detail = fmt.Sprintf("探活服务HTTP %d，无法确认令牌状态", status)
		}
		return ProbeResult{"transient", detail, latency}
	}
	state := strings.ToLower(guardText(result["status"]))
	if result["error"] == nil && (state == "active" || state == "ok" || state == "success" || state == "succeeded") {
		return ProbeResult{"ok", "探活成功", latency}
	}
	obj, _ := result["error"].(map[string]any)
	switch strings.ToLower(guardText(obj["code"])) {
	case "auth_failed", "invalid_token", "token_invalid", "unauthorized", "requires_relogin", "invalid_grant", "revoked":
		return ProbeResult{"auth", "探活报告令牌失效", latency}
	}
	return ProbeResult{"transient", "探活报告临时或未知异常", latency}
}
func (c *Client) Relogin(ctx context.Context, cfg Config, a ReloginAccount) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	result, _, err := c.request(ctx, cfg.ReloginEndpoint, "relogin", cfg.ReloginHeaders, map[string]any{"action": "start", "email": a.Email, "auth_mode": "password_2fa", "password": a.Password, "mfa_secret": a.MFASecret}, 8<<20)
	if err != nil {
		return nil, errors.New("重登请求或响应处理失败")
	}
	if result["error"] != nil {
		return nil, errors.New("重登服务返回失败")
	}
	credential, ok := result["credential"].(map[string]any)
	if !ok {
		return nil, errors.New("重登未返回凭据")
	}
	out := map[string]any{}
	for _, key := range []string{"access_token", "refresh_token", "id_token"} {
		value, ok := credential[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, errors.New("重登返回的凭据不完整")
		}
		out[key] = value
	}
	for _, key := range []string{"expires_at", "token_type"} {
		if v, ok := credential[key]; ok {
			switch v.(type) {
			case string, float64:
				out[key] = v
			}
		}
	}
	return out, nil
}
func (c *Client) Notify(ctx context.Context, cfg Config, title, body string, enabled bool) {
	_ = c.SendNotification(ctx, cfg, title, body, enabled)
}

// SendNotification reports transport/HTTP failures without retaining response text.
func (c *Client) SendNotification(ctx context.Context, cfg Config, title, body string, enabled bool) error {
	if !enabled || cfg.BarkKey == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, _ := json.Marshal(map[string]any{"device_key": cfg.BarkKey, "title": title, "body": truncateGuardText(body, 180), "group": "codex", "level": "timeSensitive", "sound": "bell", "isArchive": 1})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.day.app/push", bytes.NewReader(data))
	if err != nil {
		return errors.New("通知请求构造失败")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("通知服务请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("通知服务拒绝请求")
	}
	return nil
}
