package plugins

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("plugin setting not found")
var ErrUnknownPlugin = errors.New("unknown plugin")

// Setting is the durable, per-plugin control plane state. Flags are plugin
// specific and intentionally do not share a global settings document.
type Setting struct {
	ID               string          `json:"id"`
	Version          string          `json:"version"`
	SourceSHA        string          `json:"source_sha"`
	SDKCompatibility string          `json:"sdk_compatibility"`
	UpdateMode       string          `json:"update_mode"`
	Enabled          bool            `json:"enabled"`
	Flags            map[string]bool `json:"flags,omitempty"`
}

type Store interface {
	Get(context.Context, string) (Setting, error)
	Put(context.Context, Setting) error
}

type Registry struct {
	store    Store
	defaults map[string]Setting
	mu       sync.RWMutex
	cache    map[string]Setting
	cacheAt  map[string]time.Time
}

var defaultSettings = []Setting{
	{ID: "auto-config", Version: "1.0.0", SourceSHA: "2a0948c4bb", SDKCompatibility: "plugins/v1", UpdateMode: "compiled", Enabled: true},
	{ID: "priority-scheduling", Version: "1.0.0", SourceSHA: "2a0948c4bb", SDKCompatibility: "plugins/v1", UpdateMode: "compiled", Enabled: true},
	{ID: "quality-ops", Version: "1.0.0", SourceSHA: "e557d171", SDKCompatibility: "plugins/v1", UpdateMode: "compiled", Enabled: true},
	{ID: "account-ops", Version: "1.0.0", SourceSHA: "e557d171", SDKCompatibility: "plugins/v1", UpdateMode: "compiled", Enabled: true},
	{ID: "token-guard", Version: "1.0.0", SourceSHA: "e557d171", SDKCompatibility: "plugins/v1", UpdateMode: "compiled", Enabled: true},
	{ID: "credential-ops", Version: "1.0.0", SourceSHA: "2a0948c4bb", SDKCompatibility: "plugins/v1", UpdateMode: "compiled", Enabled: true},
	{ID: "pelican-tests", Version: "1.0.0", SourceSHA: "2a0948c4bb", SDKCompatibility: "plugins/v1", UpdateMode: "compiled", Enabled: true},
}

func NewRegistry(store Store) *Registry {
	defaults := make(map[string]Setting, len(defaultSettings))
	for _, setting := range defaultSettings {
		defaults[setting.ID] = setting
	}
	return &Registry{store: store, defaults: defaults, cache: make(map[string]Setting), cacheAt: make(map[string]time.Time)}
}

func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.defaults))
	for id := range r.defaults {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (r *Registry) Get(ctx context.Context, id string) (Setting, error) {
	defaultSetting, ok := r.defaults[id]
	if !ok {
		return Setting{}, ErrUnknownPlugin
	}
	if r.store == nil {
		return Setting{}, errors.New("plugin store is not configured")
	}
	r.mu.RLock()
	cached, cachedOK := r.cache[id]
	fresh := time.Since(r.cacheAt[id]) < 30*time.Second
	r.mu.RUnlock()
	if cachedOK && fresh {
		return cloneSetting(cached), nil
	}
	setting, err := r.store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		setting = cloneSetting(defaultSetting)
	} else if err != nil {
		return Setting{}, err
	}
	if setting.Version == "" {
		setting.Version = defaultSetting.Version
	}
	if setting.SourceSHA == "" {
		setting.SourceSHA = defaultSetting.SourceSHA
	}
	if setting.SDKCompatibility == "" {
		setting.SDKCompatibility = defaultSetting.SDKCompatibility
	}
	if setting.UpdateMode == "" {
		setting.UpdateMode = defaultSetting.UpdateMode
	}
	setting = normalize(setting)
	r.mu.Lock()
	r.cache[id] = setting
	r.cacheAt[id] = time.Now()
	r.mu.Unlock()
	return setting, nil
}

func (r *Registry) List(ctx context.Context) ([]Setting, error) {
	settings := make([]Setting, 0, len(r.defaults))
	for _, id := range r.IDs() {
		setting, err := r.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		settings = append(settings, setting)
	}
	return settings, nil
}

// Enabled is the hot-path gate required by plugin consumers. Persistence
// failures fail closed so disabled or unavailable controls cannot mutate data.
func (r *Registry) Enabled(ctx context.Context, id string) bool {
	setting, err := r.Get(ctx, id)
	return err == nil && setting.Enabled
}

func (r *Registry) Set(ctx context.Context, setting Setting) error {
	if _, ok := r.defaults[setting.ID]; !ok {
		return ErrUnknownPlugin
	}
	setting = normalize(setting)
	if setting.Version == "" {
		setting.Version = r.defaults[setting.ID].Version
	}
	if r.store == nil {
		return errors.New("plugin store is not configured")
	}
	if err := r.store.Put(ctx, setting); err != nil {
		return err
	}
	r.mu.Lock()
	r.cache[setting.ID] = setting
	r.cacheAt[setting.ID] = time.Now()
	r.mu.Unlock()
	return nil
}

func normalize(setting Setting) Setting {
	if setting.Flags == nil {
		setting.Flags = map[string]bool{}
	}
	return cloneSetting(setting)
}

func cloneSetting(setting Setting) Setting {
	flags := make(map[string]bool, len(setting.Flags))
	for key, value := range setting.Flags {
		flags[key] = value
	}
	setting.Flags = flags
	return setting
}
