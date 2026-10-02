// Adapted from Sub2API (LGPL-3.0); see SOURCE.md.
package tokenguard

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func applyGuardRequestHeaders(req *http.Request, endpoint, kind string, extra map[string]string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson, application/json")
	origin := guardOrigin(endpoint)
	if origin != "" {
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin+"/")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/131.0")
	for name, value := range extra {
		// 支持 {{uuid}} 占位符：每次请求生成新的随机值，便于需要客户端标识的接口。
		if strings.Contains(value, "{{uuid}}") {
			value = strings.ReplaceAll(value, "{{uuid}}", guardRandomID())
		}
		req.Header.Set(name, value)
	}
}

func normalizeGuardHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for name, value := range in {
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			continue
		}
		if len(out) >= 20 {
			break
		}
		out[name] = truncateGuardText(value, 512)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validateGuardHeaders(in map[string]string, field string) error {
	for name, value := range in {
		if !validGuardHeaderName(name) {
			return fmt.Errorf("%s 包含非法请求头名称: %s", field, name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s 的请求头 %s 不能包含换行", field, name)
		}
	}
	return nil
}

func validGuardHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

func guardOrigin(raw string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return parsed.Scheme + "://" + parsed.Host
	}
	return raw
}

func guardRandomID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer[:4]) + "-" + hex.EncodeToString(buffer[4:6]) + "-" + hex.EncodeToString(buffer[6:8]) +
		"-" + hex.EncodeToString(buffer[8:10]) + "-" + hex.EncodeToString(buffer[10:])
}

func parseGuardNDJSONResult(raw []byte) (map[string]any, error) {
	var result map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if guardText(event["type"]) != "result" {
			continue
		}
		if payload, ok := event["payload"].(map[string]any); ok {
			result = payload
		}
	}
	if result == nil {
		return nil, errors.New("响应缺少 result 事件")
	}
	return result, nil
}

func guardText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func truncateGuardText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func containsGuardAny(haystack string, needles []string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

func firstNonEmptyGuard(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
