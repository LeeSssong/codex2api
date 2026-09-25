// Adapted from Sub2API (LGPL-3.0); see SOURCE.md.
package tokenguard

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

const SecretMask = "********"

func DefaultConfig() Config {
	return Config{GroupIDs: []int64{}, ReloginAccounts: []ReloginAccount{}, ProbeHeaders: map[string]string{}, ReloginHeaders: map[string]string{}, IntervalSeconds: 300, ProbeModel: "gpt-6-astra", ProbeTimeoutSeconds: 240, ProbeConcurrency: 6, MaxProbePerCycle: 12, FailStreakThreshold: 1}
}
func PublicConfig(c Config) Config {
	mask := func(v string) string {
		if v != "" {
			return SecretMask
		}
		return ""
	}
	headers := func(h map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range h {
			out[k] = mask(v)
		}
		return out
	}
	c.ProbeHeaders = headers(c.ProbeHeaders)
	c.ReloginHeaders = headers(c.ReloginHeaders)
	c.BarkKey = mask(c.BarkKey)
	c.GroupIDs = append([]int64{}, c.GroupIDs...)
	c.ReloginAccounts = append([]ReloginAccount{}, c.ReloginAccounts...)
	for i := range c.ReloginAccounts {
		c.ReloginAccounts[i].Password = mask(c.ReloginAccounts[i].Password)
		c.ReloginAccounts[i].MFASecret = mask(c.ReloginAccounts[i].MFASecret)
	}
	return c
}
func RestoreSecrets(c, old Config) Config {
	restore := func(v, p string) string {
		if strings.TrimSpace(v) == "" || v == SecretMask {
			return p
		}
		return v
	}
	headers := func(h, prev map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range prev {
			out[http.CanonicalHeaderKey(k)] = v
		}
		for k, v := range h {
			key := http.CanonicalHeaderKey(strings.TrimSpace(k))
			out[key] = restore(v, out[key])
		}
		return out
	}
	c.ProbeHeaders = headers(c.ProbeHeaders, old.ProbeHeaders)
	c.ReloginHeaders = headers(c.ReloginHeaders, old.ReloginHeaders)
	c.BarkKey = restore(c.BarkKey, old.BarkKey)
	if c.ReloginAccounts == nil {
		c.ReloginAccounts = append([]ReloginAccount{}, old.ReloginAccounts...)
	} else {
		c.ReloginAccounts = append([]ReloginAccount{}, c.ReloginAccounts...)
	}
	for i := range c.ReloginAccounts {
		for _, p := range old.ReloginAccounts {
			if c.ReloginAccounts[i].AccountID == p.AccountID && strings.EqualFold(strings.TrimSpace(c.ReloginAccounts[i].Email), strings.TrimSpace(p.Email)) {
				c.ReloginAccounts[i].Password = restore(c.ReloginAccounts[i].Password, p.Password)
				c.ReloginAccounts[i].MFASecret = restore(c.ReloginAccounts[i].MFASecret, p.MFASecret)
				break
			}
		}
	}
	return c
}
func Normalize(c Config) Config {
	c.ProbeEndpoint = strings.TrimRight(strings.TrimSpace(c.ProbeEndpoint), "/")
	c.ReloginEndpoint = strings.TrimRight(strings.TrimSpace(c.ReloginEndpoint), "/")
	c.ProbeModel = strings.TrimSpace(c.ProbeModel)
	c.BarkKey = strings.TrimSpace(c.BarkKey)
	seen := map[int64]bool{}
	groups := []int64{}
	for _, id := range c.GroupIDs {
		if !seen[id] {
			groups = append(groups, id)
			seen[id] = true
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
	c.GroupIDs = groups
	for i := range c.ReloginAccounts {
		c.ReloginAccounts[i].Email = strings.ToLower(strings.TrimSpace(c.ReloginAccounts[i].Email))
	}
	return c
}
func ValidateConfig(c Config) error {
	if c.IntervalSeconds < 30 || c.IntervalSeconds > 86400 {
		return errors.New("巡检间隔需要在30到86400秒之间")
	}
	if c.ProbeTimeoutSeconds < 5 || c.ProbeTimeoutSeconds > 900 {
		return errors.New("探活超时需要在5到900秒之间")
	}
	if c.ProbeConcurrency < 1 || c.ProbeConcurrency > 16 {
		return errors.New("并发探活数需要在1到16之间")
	}
	if c.MaxProbePerCycle < 1 || c.MaxProbePerCycle > 100 {
		return errors.New("每轮最多探测账号数需要在1到100之间")
	}
	if c.FailStreakThreshold < 1 || c.FailStreakThreshold > 10 {
		return errors.New("连续失效阈值需要在1到10之间")
	}
	if len(c.ProbeModel) > 200 || len(c.GroupIDs) > 1000 || len(c.ReloginAccounts) > 1000 || len(c.BarkKey) > 512 {
		return errors.New("配置字段超过长度限制")
	}
	for _, id := range c.GroupIDs {
		if id <= 0 {
			return errors.New("守护分组ID必须为正整数")
		}
	}
	validateURL := func(raw string) bool {
		u, e := url.Parse(raw)
		return e == nil && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "http" || u.Scheme == "https") && len(raw) <= 2048
	}
	if (c.Enabled || c.ProbeEndpoint != "") && !validateURL(c.ProbeEndpoint) {
		return errors.New("probe_endpoint需要是合法的http/https地址")
	}
	if c.Enabled && c.ProbeModel == "" {
		return errors.New("启用守护时必须填写探活模型")
	}
	if (c.AutoRelogin || c.ReloginEndpoint != "") && !validateURL(c.ReloginEndpoint) {
		return errors.New("relogin_endpoint需要是合法的http/https地址")
	}
	if c.AutoRelogin && len(c.ReloginAccounts) == 0 {
		return errors.New("自动重登需要明确账号映射")
	}
	for _, h := range []map[string]string{c.ProbeHeaders, c.ReloginHeaders} {
		if len(h) > 20 {
			return errors.New("请求头最多20项")
		}
		for name, v := range h {
			if !validGuardHeaderName(name) || len(name) > 128 || len(v) > 512 || strings.ContainsAny(v, "\r\n") {
				return errors.New("请求头配置不合法")
			}
		}
	}
	seen := map[int64]bool{}
	for _, a := range c.ReloginAccounts {
		if a.AccountID <= 0 || seen[a.AccountID] || !strings.Contains(a.Email, "@") || len(a.Email) > 320 || strings.TrimSpace(a.Password) == "" || a.Password == SecretMask || len(a.Password) > 4096 || len(a.MFASecret) > 1024 || a.MFASecret == SecretMask {
			return errors.New("重登账号映射或凭据不合法")
		}
		seen[a.AccountID] = true
	}
	return nil
}
