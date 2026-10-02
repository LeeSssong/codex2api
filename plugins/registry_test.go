package plugins

import (
	"context"
	"testing"
)

type memoryStore struct{ values map[string]Setting }

func (s *memoryStore) Get(_ context.Context, id string) (Setting, error) {
	value, ok := s.values[id]
	if !ok {
		return Setting{}, ErrNotFound
	}
	return value, nil
}

func (s *memoryStore) Put(_ context.Context, value Setting) error {
	s.values[value.ID] = value
	return nil
}

func TestRegistryDefaultsAndIndependentFlags(t *testing.T) {
	store := &memoryStore{values: map[string]Setting{}}
	registry := NewRegistry(store)
	if !registry.Enabled(context.Background(), "quality-ops") {
		t.Fatal("quality-ops should be enabled by default")
	}
	if err := registry.Set(context.Background(), Setting{ID: "quality-ops", Version: "2.1.0", Enabled: false, Flags: map[string]bool{"alerts": true}}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if registry.Enabled(context.Background(), "quality-ops") {
		t.Fatal("disabled plugin reported enabled")
	}
	if !registry.Enabled(context.Background(), "token-guard") {
		t.Fatal("changing one plugin changed another")
	}
	setting, err := registry.Get(context.Background(), "quality-ops")
	if err != nil || setting.Version != registry.defaults["quality-ops"].Version || !setting.Flags["alerts"] {
		t.Fatalf("stored setting = %+v, err=%v", setting, err)
	}
}

func TestRegistryInstalledMetadataOverridesPersistedAndSubmittedVersions(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{values: map[string]Setting{"quality-ops": {ID: "quality-ops", Version: "old", SourceSHA: "old-source", SDKCompatibility: "old-sdk", UpdateMode: "old-mode", Enabled: false, Flags: map[string]bool{"alerts": true}}}}
	registry := NewRegistry(store)
	installed := registry.defaults["quality-ops"]
	installed.Version = "2.0.0"
	installed.SourceSHA = "new-installed-source"
	installed.SDKCompatibility = "plugins/v2"
	installed.UpdateMode = "new-mode"
	registry.defaults[installed.ID] = installed
	assertInstalled := func(s Setting) {
		t.Helper()
		if s.ID != installed.ID || s.Version != installed.Version || s.SourceSHA != installed.SourceSHA || s.SDKCompatibility != installed.SDKCompatibility || s.UpdateMode != installed.UpdateMode {
			t.Fatalf("metadata did not match installed build: %+v", s)
		}
		if s.Enabled || !s.Flags["alerts"] {
			t.Fatal("metadata normalization discarded controls")
		}
	}
	setting, err := registry.Get(ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertInstalled(setting)
	setting.Version = "forged"
	setting.SourceSHA = "forged"
	setting.SDKCompatibility = "forged"
	setting.UpdateMode = "forged"
	if err := registry.Set(ctx, setting); err != nil {
		t.Fatal(err)
	}
	assertInstalled(store.values[installed.ID])
	setting, err = registry.Get(ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertInstalled(setting)
}

func TestRegistryRejectsUnknownIDs(t *testing.T) {
	registry := NewRegistry(&memoryStore{values: map[string]Setting{}})
	if err := registry.Set(context.Background(), Setting{ID: "unknown", Enabled: true}); err != ErrUnknownPlugin {
		t.Fatalf("Set unknown error = %v", err)
	}
	if registry.Enabled(context.Background(), "unknown") {
		t.Fatal("unknown plugin reported enabled")
	}
}
