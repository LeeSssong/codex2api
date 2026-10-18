package fixedegress

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/codex2api/database"
	"github.com/codex2api/security"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func ResourceID(pool, provider, endpoint string) string {
	s := sha256.Sum256([]byte(pool + "\x00" + provider + "\x00" + endpoint))
	return hex.EncodeToString(s[:16])
}
func (m *Manager) Import(ctx context.Context, pool, protocol string, format int, lines string) error {
	if _, e := m.db.GetFixedEgressPool(ctx, pool); e != nil {
		return errors.New("pool not found")
	}
	rows := strings.FieldsFunc(lines, func(r rune) bool { return r == '\n' || r == '\r' })
	if len(rows) == 0 || len(rows) > 500 {
		return errors.New("import requires 1–500 proxy rows")
	}
	items := []database.FixedEgressResource{}
	for _, line := range rows {
		p, e := NormalizeProxyLine(line, protocol, format)
		if e != nil {
			return e
		}
		items = append(items, database.FixedEgressResource{ID: ResourceID(pool, "", p), PoolID: pool, ProxyURL: p, State: "pending"})
	}
	for _, r := range items {
		if _, e := m.db.GetFixedEgressResource(ctx, r.ID); e == nil {
			continue
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e := m.db.SaveFixedEgressResource(ctx, r); e != nil {
			return e
		}
	}
	return nil
}
func (m *Manager) TestResource(ctx context.Context, id string) error {
	return m.testResource(ctx, id, "https://api.ipify.org?format=json")
}
func (m *Manager) testResource(ctx context.Context, id, probeURL string) error {
	r, e := m.db.GetFixedEgressResource(ctx, id)
	if e != nil {
		return e
	}
	if r.State == "retired" {
		return errors.New("resource retired")
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	proxyAddress, parseErr := security.ParseProxyURL(r.ProxyURL)
	if parseErr != nil {
		return errors.New("invalid proxy configuration")
	}
	tr.Proxy = http.ProxyURL(proxyAddress)
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, "GET", probeURL, nil)
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("proxy probe failed; previous health retained")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 407 {
		_ = m.db.SetFixedEgressResourceHealth(ctx, id, r.ProxyURL, "", "quarantined")
		return errors.New("proxy authentication failed")
	}
	if resp.StatusCode != 200 {
		return errors.New("probe inconclusive; previous health retained")
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if e != nil || len(b) > 4096 {
		return errors.New("probe response invalid")
	}
	var obj struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(b, &obj) != nil {
		obj.IP = strings.TrimSpace(string(b))
	}
	ip, e := netip.ParseAddr(obj.IP)
	if e != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return errors.New("probe did not return public exit IP")
	}
	if e = m.db.SetFixedEgressResourceHealth(ctx, id, r.ProxyURL, ip.String(), "healthy"); e != nil {
		return e
	}
	updated, e := m.db.GetFixedEgressResource(ctx, id)
	if e == nil && updated.State == "quarantined" {
		return errors.New("bound exit IP changed; resource quarantined")
	}
	return e
}
func providerFormat(p database.FixedEgressProvider) int { n, _ := strconv.Atoi(p.AuthFormat); return n }
func (m *Manager) SyncProvider(ctx context.Context, id, pool string) error {
	p, e := m.db.GetFixedEgressProvider(ctx, id)
	if e != nil {
		return e
	}
	c, e := NewCliProxyClient(p.BaseURL)
	if e != nil {
		return e
	}
	entries, e := c.Online(ctx, p.APIKey, p.Protocol, providerFormat(p))
	if e != nil {
		return e
	}
	return m.syncInventory(ctx, p, pool, entries)
}
func (m *Manager) syncInventory(ctx context.Context, p database.FixedEgressProvider, pool string, entries []CliProxyEndpoint) error {
	if _, e := m.db.GetFixedEgressPool(ctx, pool); e != nil {
		return e
	}
	existing, e := m.db.ListFixedEgress(ctx)
	if e != nil {
		return e
	}
	tasks, e := m.db.ListFixedEgressTasks(ctx)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.Port < p.PortStart || entry.Port > p.PortEnd {
			continue
		}
		id := ResourceID(pool, p.ID, strconv.Itoa(entry.Port))
		found := false
		for _, r := range existing {
			if r.ProviderID == p.ID && r.ProviderPort == entry.Port {
				found = true
				id = r.ID
				if r.ProxyURL != entry.ProxyURL {
					_ = m.db.SetFixedEgressResourceHealth(ctx, r.ID, r.ProxyURL, "", "quarantined")
				}
				break
			}
		}
		if !found {
			if e = m.db.SaveFixedEgressResource(ctx, database.FixedEgressResource{ID: id, PoolID: pool, ProviderID: p.ID, ProviderPort: entry.Port, ProxyURL: entry.ProxyURL, State: "pending"}); e != nil {
				return e
			}
		}
		for _, t := range tasks {
			if t.ProviderID == p.ID && t.Port == entry.Port && t.State == "unknown" {
				if e = m.db.FinalizeFixedEgressTask(ctx, t.ID, id, entry.Port, "reconciled"); e != nil {
					return e
				}
			}
		}
	}
	for _, t := range tasks {
		if t.ProviderID != p.ID || t.State != "stop_unknown" {
			continue
		}
		present := false
		for _, entry := range entries {
			if entry.Port == t.Port {
				present = true
				break
			}
		}
		if !present {
			if e := m.db.CompleteFixedEgressStop(ctx, t.ResourceID); e != nil {
				return e
			}
		}
	}
	// Missing entries never delete or reassign resources. Unknown starts remain
	// reserved if not visible; supplier semantics may be eventually consistent.
	return nil
}
func (m *Manager) StartProvider(ctx context.Context, id, pool string) error {
	p, e := m.db.GetFixedEgressProvider(ctx, id)
	if e != nil {
		return e
	}
	if !p.Enabled {
		return errors.New("provider disabled")
	}
	if _, e = m.db.GetFixedEgressPool(ctx, pool); e != nil {
		return e
	}
	c, e := NewCliProxyClient(p.BaseURL)
	if e != nil {
		return e
	}
	online, e := c.Online(ctx, p.APIKey, p.Protocol, providerFormat(p))
	if e != nil {
		return e
	}
	resources, e := m.db.ListFixedEgress(ctx)
	if e != nil {
		return e
	}
	tasks, e := m.db.ListFixedEgressTasks(ctx)
	if e != nil {
		return e
	}
	used := map[int]bool{}
	for _, r := range online {
		used[r.Port] = true
	}
	for _, r := range resources {
		if r.ProviderID == id {
			used[r.ProviderPort] = true
		}
	}
	for _, t := range tasks {
		if t.ProviderID == id && t.State != "stopped" {
			used[t.Port] = true
		}
	}
	port := 0
	for n := p.PortStart; n <= p.PortEnd; n++ {
		if !used[n] {
			port = n
			break
		}
	}
	if port == 0 {
		return errors.New("no unused managed provider port")
	}
	task := database.FixedEgressTask{ID: ResourceID(id, strconv.Itoa(port), strconv.FormatInt(time.Now().UnixNano(), 10)), ProviderID: id, Port: port}
	if e = m.db.ReserveFixedEgressTask(ctx, task); e != nil {
		return errors.New("provider capacity, frequency or reservation limit reached")
	}
	entries, e := c.Start(ctx, CliProxyRequest{Key: p.APIKey, Port: port, Country: p.Country, City: p.City, IPs: strings.Join(p.WhitelistIPs, ","), Protocol: p.Protocol, Format: providerFormat(p)})
	if e != nil {
		return e
	}
	if len(entries) != 1 || entries[0].Port != port {
		return errors.New("start result port mapping unconfirmed; synchronize online inventory")
	}
	rid := ResourceID(pool, id, strconv.Itoa(port))
	if e = m.db.SaveFixedEgressResource(ctx, database.FixedEgressResource{ID: rid, PoolID: pool, ProviderID: id, ProviderPort: port, ProxyURL: entries[0].ProxyURL, State: "pending"}); e != nil {
		return e
	}
	return m.db.FinalizeFixedEgressTask(ctx, task.ID, rid, port, "started")
}
func (m *Manager) StopResource(ctx context.Context, id string) error {
	r, e := m.db.GetFixedEgressResource(ctx, id)
	if e != nil {
		return e
	}
	if !r.Owned || r.ProviderID == "" || r.ProviderPort == 0 {
		return errors.New("resource not created by this manager")
	}
	// Retirement must precede network operation to exclude new claims. Caller
	// requires no active enrolled traffic before remote release.
	if e = m.db.RetireFixedEgressResource(ctx, id, true); e != nil {
		return e
	}
	p, e := m.db.GetFixedEgressProvider(ctx, r.ProviderID)
	if e != nil {
		return e
	}
	c, e := NewCliProxyClient(p.BaseURL)
	if e != nil {
		return e
	}
	if e = c.Stop(ctx, p.APIKey, r.ProviderPort); e != nil {
		return e
	}
	return m.db.CompleteFixedEgressStop(ctx, id)
}
func MaskEndpoint(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
