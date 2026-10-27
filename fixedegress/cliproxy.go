package fixedegress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxProviderBody = 1 << 20

// CliProxyClient handles management only; business requests never use this API.
// Start and Stop have remote side effects and are deliberately never retried.
type CliProxyClient struct {
	base   string
	client *http.Client
}
type CliProxyRequest struct {
	Key                                 string
	Port                                int
	Country, State, City, IPs, Protocol string
	Format                              int
}
type CliProxyEndpoint struct {
	ProxyURL string
	Port     int
}

func NewCliProxyClient(base string) (*CliProxyClient, error) {
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid HTTPS provider base URL")
	}
	if ip, e := netip.ParseAddr(u.Hostname()); e == nil && (!ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		return nil, errors.New("provider address must be public")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, errors.New("invalid provider destination")
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil {
			return nil, errors.New("provider DNS failed")
		}
		if len(ips) == 0 {
			return nil, errors.New("provider DNS empty")
		}
		for _, ip := range ips {
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				return nil, errors.New("provider destination must be public")
			}
		}
		d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		for _, ip := range ips {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, errors.New("provider connection failed")
	}
	return newCliProxyClient(strings.TrimRight(base, "/"), &http.Client{Transport: tr, Timeout: 15 * time.Second}), nil
}
func newCliProxyClient(base string, client *http.Client) *CliProxyClient {
	c := *client
	c.Timeout = 15 * time.Second
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &CliProxyClient{strings.TrimRight(base, "/"), &c}
}
func (c *CliProxyClient) call(ctx context.Context, path string, q url.Values) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
	if e != nil {
		return nil, errors.New("provider request invalid")
	}
	req.Header.Set("User-Agent", "Codex2API-ProxyManager/1.0")
	resp, e := c.client.Do(req)
	if e != nil {
		return nil, errors.New("provider response unconfirmed; reconcile inventory before retry")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("provider rejected management request; inspect supplier dashboard")
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody+1))
	if e != nil || len(body) > maxProviderBody {
		return nil, errors.New("provider response invalid or too large")
	}
	return body, nil
}
func (c *CliProxyClient) Online(ctx context.Context, key, protocol string, format int) ([]CliProxyEndpoint, error) {
	b, e := c.call(ctx, "/online", url.Values{"key": {key}})
	if e != nil {
		return nil, e
	}
	return ParseCliProxyInventory(b, protocol, format)
}
func (c *CliProxyClient) Start(ctx context.Context, p CliProxyRequest) ([]CliProxyEndpoint, error) {
	if p.Key == "" || p.Port < 1 || p.Port > 65535 || p.Format < 1 || p.Format > 3 {
		return nil, errors.New("invalid provider start parameters")
	}
	q := url.Values{"key": {p.Key}, "port": {strconv.Itoa(p.Port)}, "num": {"1"}, "country": {p.Country}, "type": {strconv.Itoa(p.Format)}, "format": {"n"}}
	if p.State != "" {
		q.Set("state", p.State)
	}
	if p.City != "" {
		q.Set("city", p.City)
	}
	if p.IPs != "" {
		q.Set("ips", p.IPs)
	}
	b, e := c.call(ctx, "/start", q)
	if e != nil {
		return nil, e
	}
	out, e := ParseCliProxyInventory(b, p.Protocol, p.Format)
	if e != nil || len(out) != 1 {
		return nil, errors.New("provider start result unconfirmed; reconcile inventory")
	}
	return out, nil
}
func (c *CliProxyClient) Stop(ctx context.Context, key string, port int) error {
	if key == "" || port < 1 || port > 65535 {
		return errors.New("individual provider port required")
	}
	b, e := c.call(ctx, "/stop", url.Values{"key": {key}, "port": {strconv.Itoa(port)}})
	if e != nil {
		return e
	}
	s := strings.ToLower(strings.TrimSpace(string(b)))
	if s == "success" || s == "ok" || s == "true" || s == "1" {
		return nil
	}
	var obj map[string]any
	if json.Unmarshal(b, &obj) == nil && obj["success"] == true && obj["error"] == nil {
		return nil
	}
	return errors.New("provider stop result unconfirmed; reconcile inventory")
}

// ParseCliProxyInventory accepts explicitly recognizable text/JSON inventory.
// Unknown success envelopes never become an authoritative empty inventory.
func ParseCliProxyInventory(body []byte, protocol string, format int) ([]CliProxyEndpoint, error) {
	if len(body) > maxProviderBody || (protocol != "http" && protocol != "https" && protocol != "socks5" && protocol != "socks5h") || format < 1 || format > 3 {
		return nil, errors.New("invalid provider inventory configuration")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, errors.New("empty provider response")
	}
	var entries []any
	if body[0] == '[' || body[0] == '{' {
		var value any
		if json.Unmarshal(body, &value) != nil {
			return nil, errors.New("invalid provider JSON")
		}
		if obj, ok := value.(map[string]any); ok {
			if obj["error"] != nil || obj["success"] == false {
				return nil, errors.New("provider returned error")
			}
			found := false
			for _, key := range []string{"resources", "data", "proxies", "list"} {
				if a, ok := obj[key].([]any); ok {
					entries = a
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("unrecognized provider inventory")
			}
		} else {
			var ok bool
			entries, ok = value.([]any)
			if !ok {
				return nil, errors.New("unrecognized provider inventory")
			}
		}
	} else {
		for _, s := range strings.FieldsFunc(string(body), func(r rune) bool { return r == '\n' || r == '\r' }) {
			entries = append(entries, strings.TrimSpace(s))
		}
	}
	out := make([]CliProxyEndpoint, 0, len(entries))
	seen := map[string]bool{}
	for _, item := range entries {
		var line string
		switch v := item.(type) {
		case string:
			line = v
		case map[string]any:
			for _, key := range []string{"proxy_url", "proxy", "url"} {
				if x, ok := v[key].(string); ok && x != "" {
					line = x
					break
				}
			}
			if line == "" {
				host, _ := v["host"].(string)
				if host == "" {
					host, _ = v["ip"].(string)
				}
				port := ""
				switch p := v["port"].(type) {
				case float64:
					if p == float64(int(p)) {
						port = strconv.Itoa(int(p))
					}
				case string:
					port = p
				}
				user, _ := v["username"].(string)
				if user == "" {
					user, _ = v["user"].(string)
				}
				pass, _ := v["password"].(string)
				if host == "" || port == "" {
					return nil, errors.New("unrecognized provider resource")
				}
				hostport := net.JoinHostPort(strings.Trim(host, "[]"), port)
				u := url.URL{Scheme: protocol, Host: hostport}
				if user != "" {
					u.User = url.UserPassword(user, pass)
				}
				line = u.String()
			}
		default:
			return nil, errors.New("unrecognized provider resource")
		}
		normalized, e := NormalizeProxyLine(line, protocol, format)
		if e != nil {
			return nil, errors.New("invalid provider proxy address")
		}
		u, _ := url.Parse(normalized)
		p, _ := strconv.Atoi(u.Port())
		if !seen[normalized] {
			out = append(out, CliProxyEndpoint{normalized, p})
			seen[normalized] = true
		}
	}
	return out, nil
}

func NormalizeProxyLine(raw, protocol string, format int) (string, error) {
	if protocol != "http" && protocol != "https" && protocol != "socks5" && protocol != "socks5h" {
		return "", errors.New("unsupported proxy protocol")
	}
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		switch format {
		case 1:
			raw = protocol + "://" + raw
		case 2:
			host, rest := "", ""
			if strings.HasPrefix(raw, "[") {
				i := strings.Index(raw, "]:")
				if i < 0 {
					return "", errors.New("invalid proxy address")
				}
				host = raw[:i+1]
				rest = raw[i+2:]
			} else {
				p := strings.SplitN(raw, ":", 2)
				if len(p) != 2 {
					return "", errors.New("invalid proxy address")
				}
				host = p[0]
				rest = p[1]
			}
			parts := strings.SplitN(rest, ":", 3)
			if len(parts) != 3 {
				return "", errors.New("invalid proxy credentials")
			}
			u := url.URL{Scheme: protocol, Host: host + ":" + parts[0], User: url.UserPassword(parts[1], parts[2])}
			raw = u.String()
		case 3:
			at := strings.LastIndex(raw, "@")
			if at <= 0 {
				return "", errors.New("invalid proxy credentials")
			}
			parts := strings.SplitN(raw[:at], ":", 2)
			if len(parts) != 2 {
				return "", errors.New("invalid proxy credentials")
			}
			u := url.URL{Scheme: protocol, Host: raw[at+1:], User: url.UserPassword(parts[0], parts[1])}
			raw = u.String()
		default:
			return "", errors.New("invalid proxy format")
		}
	}
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.Port() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid proxy address")
	}
	p, e := strconv.Atoi(u.Port())
	if e != nil || p < 1 || p > 65535 {
		return "", errors.New("invalid proxy port")
	}
	if u.Scheme != protocol {
		return "", errors.New("proxy protocol does not match configuration")
	}
	if format == 1 && u.User != nil {
		return "", errors.New("unexpected proxy credentials in host:port format")
	}
	return u.String(), nil
}
