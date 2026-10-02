package proxy

import (
	"encoding/json"
	"errors"
	"github.com/codex2api/auth"
	"github.com/codex2api/smartops"
	"strings"
	"sync/atomic"
)

type activeBPSPolicy interface{ QualityBPSTransportDefaults() ([]byte, bool) }

func activeSmartOpsBPS(account *auth.Account) (smartops.BPSDefaults, bool) {
	provider, ok := any(account).(activeBPSPolicy)
	if !ok {
		return smartops.BPSDefaults{}, false
	}
	raw, active := provider.QualityBPSTransportDefaults()
	var c smartops.BPSDefaults
	if !active || json.Unmarshal(raw, &c) != nil {
		return c, false
	}
	return c, true
}

type smartOpsBPSProxyResolver struct {
	resolve func(*auth.Account, string) (string, error)
}

var smartOpsBPSProxy atomic.Pointer[smartOpsBPSProxyResolver]

func ConfigureSmartOpsBPSProxyResolver(resolve func(*auth.Account, string) (string, error)) {
	if resolve == nil {
		smartOpsBPSProxy.Store(nil)
	} else {
		smartOpsBPSProxy.Store(&smartOpsBPSProxyResolver{resolve: resolve})
	}
}

// The same transformation is used by route admission and the native executor.
func smartOpsBPSBody(account *auth.Account, body []byte) []byte {
	c, active := activeSmartOpsBPS(account)
	if !active {
		return body
	}
	return prepareSmartOpsBPSBody(c, body)
}
func prepareSmartOpsBPSBody(c smartops.BPSDefaults, body []byte) []byte {
	if c.IgnoreEncryptedContent {
		body, _ = stripInvalidEncryptedContentFromResponsesBody(body)
	}
	if !c.OmitUnsupportedTools {
		return body
	}
	var root map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil {
		return body
	}
	var omit func(any) []any
	omit = func(value any) []any {
		list, _ := value.([]any)
		out := make([]any, 0, len(list))
		for _, item := range list {
			tool, ok := item.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := tool["type"].(string)
			switch kind {
			case "function", "custom":
				out = append(out, tool)
			case "namespace":
				tool["tools"] = omit(tool["tools"])
				out = append(out, tool)
			}
		}
		return out
	}
	if value, ok := root["tools"]; ok {
		root["tools"] = omit(value)
	}
	if input, ok := root["input"].([]any); ok {
		for _, item := range input {
			if part, ok := item.(map[string]any); ok && part["type"] == "additional_tools" {
				part["tools"] = omit(part["tools"])
			}
		}
	}
	result, e := json.Marshal(root)
	if e != nil {
		return body
	}
	return result
}
func smartOpsBPSSessionProxy(account *auth.Account, c smartops.BPSDefaults, session string) (string, error) {
	if !c.SessionProxy {
		return "", nil
	}
	if c.ProxySource != "ip_pool" {
		return "", errors.New("Mihomo session proxy is unavailable in the native transport")
	}
	resolver := smartOpsBPSProxy.Load()
	if resolver == nil {
		return "", errors.New("native session proxy resolver unavailable")
	}
	if strings.TrimSpace(session) == "" {
		return "", errors.New("session proxy requires a session identity")
	}
	return resolver.resolve(account, session)
}
