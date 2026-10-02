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
	if err != nil || setting.Version != "2.1.0" || !setting.Flags["alerts"] {
		t.Fatalf("stored setting = %+v, err=%v", setting, err)
	}
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
