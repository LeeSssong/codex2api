package admin

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/egressipv6"
	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
	"time"
)

func (s importSettings) applyPreferences(seed tokenCredentialSeed, t importToken) tokenCredentialSeed {
	enabled := true
	proxy := s.proxyForToken(t)
	if s.proxySpecified || proxy != "" || t.proxySpecified {
		enabled = false
	}
	if s.ipv6Enabled != nil {
		enabled = *s.ipv6Enabled
	}
	// A source's explicit opt-out is data, not a missing preference. An import
	// form's enabled default must not silently enroll it.
	if (t.proxySpecified && t.ipv6Enabled == nil) || (t.ipv6Enabled != nil && !*t.ipv6Enabled) {
		enabled = false
	}
	if t.ipv6Enabled != nil && *t.ipv6Enabled && s.ipv6Enabled == nil {
		enabled = true
	}
	if proxy != "" {
		enabled = false
	}
	seed.newIPv6Enabled = &enabled
	seed.newTimezone = s.defaultTimezone
	if seed.newTimezone == nil {
		v := "America/New_York"
		seed.newTimezone = &v
	}
	seed.timezone = t.timezone
	// Only explicit request route overrides alter a duplicate's selection.
	if s.proxySpecified || s.ipv6Enabled != nil {
		seed.ipv6Enabled = &enabled
		seed.routeOverride = true
	}
	return seed
}
func (h *Handler) retireIPv6Selection(ctx context.Context, id int64, updates map[string]interface{}) error {
	if enabled, ok := updates[auth.IPv6EgressCredentialKey].(bool); ok && !enabled {
		if m := egressipv6.Current(); m != nil {
			return m.RetireAccount(ctx, id)
		}
		return h.db.RetireIPv6Egress(ctx, id, time.Now().Unix())
	}
	return nil
}
func (h *Handler) accountIPv6IP(row *database.AccountRow) string {
	if !row.GetCredentialBool(auth.IPv6EgressCredentialKey) || row.ProxyURL != "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ip, err := h.db.CurrentIPv6EgressIP(ctx, row.ID)
	if err != nil {
		return ""
	}
	return ip
}
func (s *importSettings) readPreferences(c *gin.Context) error {
	_, s.proxySpecified = c.GetPostForm("proxy_url")
	if value, ok := c.GetPostForm("ipv6_egress_enabled"); ok {
		v, err := strconv.ParseBool(value)
		if err != nil {
			return errors.New("invalid ipv6_egress_enabled")
		}
		s.ipv6Enabled = &v
	}
	if value, ok := c.GetPostForm("default_timezone"); ok {
		value = strings.TrimSpace(value)
		if err := validateAccountTimezone(value); err != nil {
			return err
		}
		s.defaultTimezone = &value
	}
	if s.ipv6Enabled != nil && *s.ipv6Enabled && strings.TrimSpace(s.defaultProxyURL) != "" {
		return errors.New("local IPv6 cannot be combined with proxy_url")
	}
	return nil
}

// Keep string fields compatible with existing handlers while retaining whether
// blank was explicitly selected. The alias avoids recursive unmarshaling.
func (r *addAccountReq) UnmarshalJSON(data []byte) error {
	type plain addAccountReq
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = addAccountReq(p)
	var fields map[string]json.RawMessage
	json.Unmarshal(data, &fields)
	_, r.proxySpecified = fields["proxy_url"]
	return nil
}
func (r *addATAccountReq) UnmarshalJSON(data []byte) error {
	type plain addATAccountReq
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = addATAccountReq(p)
	var fields map[string]json.RawMessage
	json.Unmarshal(data, &fields)
	_, r.proxySpecified = fields["proxy_url"]
	return nil
}
func (r *jsonAccountEntry) UnmarshalJSON(data []byte) error {
	type plain jsonAccountEntry
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = jsonAccountEntry(p)
	var fields map[string]json.RawMessage
	json.Unmarshal(data, &fields)
	_, r.proxySpecified = fields["proxy_url"]
	return nil
}
func (r *sub2apiAccountEntry) UnmarshalJSON(data []byte) error {
	type plain sub2apiAccountEntry
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = sub2apiAccountEntry(p)
	var fields map[string]json.RawMessage
	json.Unmarshal(data, &fields)
	_, r.proxySpecified = fields["proxy_url"]
	return nil
}
func (r *sub2apiAccountCredentials) UnmarshalJSON(data []byte) error {
	type plain sub2apiAccountCredentials
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = sub2apiAccountCredentials(p)
	var fields map[string]json.RawMessage
	json.Unmarshal(data, &fields)
	_, r.proxySpecified = fields["proxy_url"]
	return nil
}

func stringPtrIPv6Internal(s string) *string { return &s }

func (h *Handler) applyDuplicateImportPreferences(ctx context.Context, id int64, settings importSettings, t importToken) error {
	seed := settings.applyPreferences(tokenCredentialSeed{}, t)
	updates := map[string]interface{}{}
	proxy := database.OptionalString{}
	if seed.routeOverride {
		updates[auth.IPv6EgressCredentialKey] = *seed.ipv6Enabled
		proxy = database.OptionalString{Set: true, Value: settings.proxyForToken(t)}
	}
	if t.timezone != nil {
		updates[auth.AccountTimezoneCredentialKey] = *t.timezone
	}
	if len(updates) == 0 {
		return nil
	}
	if err := h.db.UpdateAccountSchedulerMetadata(ctx, id, database.OptionalNullInt64{}, database.OptionalNullInt64{}, database.OptionalBool{}, database.OptionalInt64Slice{}, database.OptionalStringSlice{}, database.OptionalInt64Slice{}, proxy, updates); err != nil {
		return err
	}
	if h.store != nil {
		if proxy.Set {
			h.store.ApplyAccountProxyURL(id, proxy.Value)
		}
		if seed.routeOverride {
			h.store.ApplyAccountIPv6Selection(id, *seed.ipv6Enabled)
		}
		if t.timezone != nil {
			h.store.ApplyAccountTimezone(id, *t.timezone)
		}
	}
	return h.retireIPv6Selection(ctx, id, updates)
}

// JSON entry points share the same absent/blank semantics as multipart imports.
type nativeJSONPreferences struct {
	ProxyURL          *string `json:"proxy_url"`
	IPv6EgressEnabled *bool   `json:"ipv6_egress_enabled"`
	DefaultTimezone   *string `json:"default_timezone"`
}

func (r nativeJSONPreferences) settings() (importSettings, error) {
	s := importSettings{ipv6Enabled: r.IPv6EgressEnabled, defaultTimezone: r.DefaultTimezone, proxySpecified: r.ProxyURL != nil}
	if r.ProxyURL != nil {
		s.defaultProxyURL = strings.TrimSpace(*r.ProxyURL)
	}
	if err := security.ValidateProxyURL(s.defaultProxyURL); err != nil {
		return s, err
	}
	if r.DefaultTimezone != nil {
		value := strings.TrimSpace(*r.DefaultTimezone)
		if err := validateAccountTimezone(value); err != nil {
			return s, err
		}
		s.defaultTimezone = &value
	}
	if r.IPv6EgressEnabled != nil && *r.IPv6EgressEnabled && s.defaultProxyURL != "" {
		return s, errors.New("local IPv6 cannot be combined with proxy_url")
	}
	return s, nil
}

func importPreferencesFromMap(t *importToken, source map[string]interface{}) {
	if v, ok := source["ipv6_egress_enabled"].(bool); ok {
		t.ipv6Enabled = &v
	}
	if v, ok := source["proxy_url"].(string); ok {
		t.proxyURL = strings.TrimSpace(v)
		t.proxySpecified = true
	}
	if v, ok := source["timezone"].(string); ok {
		t.timezone = &v
	}
}

func agentImportPreferences(fields *agentIdentityFields, settings importSettings) {
	pref := settings.applyPreferences(tokenCredentialSeed{}, fields.sourcePreferences)
	fields.preferences = &pref
}
