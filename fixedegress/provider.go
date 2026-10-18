package fixedegress

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type ProxyAddress struct{ Scheme, Host, Username, Password string }

func ParseProxyURL(raw string) (ProxyAddress, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.Host == "" || u.Port() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return ProxyAddress{}, fmt.Errorf("invalid proxy address")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return ProxyAddress{}, fmt.Errorf("invalid proxy port")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "socks5" && scheme != "socks5h" {
		return ProxyAddress{}, fmt.Errorf("unsupported proxy protocol")
	}
	p := ProxyAddress{Scheme: scheme, Host: u.Host}
	if u.User != nil {
		p.Username = u.User.Username()
		p.Password, _ = u.User.Password()
	}
	return p, nil
}
func ParseStaticProxy(raw string) (ProxyAddress, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		return ParseProxyURL(raw)
	}
	if at := strings.LastIndex(raw, "@"); at > 0 {
		cred := raw[:at]
		parts := strings.SplitN(cred, ":", 2)
		if len(parts) != 2 {
			return ProxyAddress{}, fmt.Errorf("invalid proxy credentials")
		}
		normalized, err := NormalizeProxyLine(raw, "http", 3)
		if err != nil {
			return ProxyAddress{}, err
		}
		return ParseProxyURL(normalized)
	}
	parts := strings.Split(raw, ":")
	if len(parts) == 2 {
		return ParseProxyURL("http://" + raw)
	}
	if len(parts) == 4 {
		normalized, err := NormalizeProxyLine(raw, "http", 2)
		if err != nil {
			return ProxyAddress{}, err
		}
		return ParseProxyURL(normalized)
	}
	return ProxyAddress{}, fmt.Errorf("invalid proxy format")
}

type OnlineResponse struct {
	Resources []struct {
		ID     string `json:"id"`
		Host   string `json:"host"`
		Port   int    `json:"port"`
		ExitIP string `json:"exit_ip"`
		Status string `json:"status"`
	} `json:"resources"`
}

func ParseOnline(body []byte) (OnlineResponse, error) {
	var out OnlineResponse
	if len(body) > 4<<20 {
		return out, fmt.Errorf("provider response too large")
	}
	if e := json.Unmarshal(body, &out); e != nil {
		return out, fmt.Errorf("invalid provider response")
	}
	if out.Resources == nil {
		return out, fmt.Errorf("unrecognized provider response")
	}
	return out, nil
}
