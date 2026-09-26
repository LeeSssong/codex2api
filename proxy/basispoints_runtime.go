package proxy

import (
	"context"
	"github.com/codex2api/database"
	"sync"
	"sync/atomic"
	"time"
)

var basispointsSettingsSnapshot atomic.Pointer[database.BasispointsSettings]
var basispointsSettingsSyncMu sync.Mutex

// SetBasispointsSettings publishes a copied configuration. Production readers
// serialize DB fetch and publication via SyncBasispointsSettings.
func SetBasispointsSettings(s database.BasispointsSettings) {
	s.Models = append([]string{}, s.Models...)
	basispointsSettingsSnapshot.Store(&s)
	UpdateRuntimeSettings(func(r RuntimeSettings) RuntimeSettings { r.CodexBasispointsEnabled = s.Enabled; return r })
}
func CurrentBasispointsSettings() database.BasispointsSettings {
	s := database.DefaultBasispointsSettings()
	if p := basispointsSettingsSnapshot.Load(); p != nil {
		s = *p
		s.Models = append([]string{}, p.Models...)
	}
	s.Enabled = CurrentRuntimeSettings().CodexBasispointsEnabled
	return s
}

// Fetch under the publication lock: an older poll cannot overwrite an admin
// save. Admin hooks re-read committed truth through this same function.
func SyncBasispointsSettings(parent context.Context, db *database.DB, publishEnabled func(bool)) error {
	basispointsSettingsSyncMu.Lock()
	defer basispointsSettingsSyncMu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	s, err := db.GetBasispointsSettings(ctx)
	if err != nil {
		s = CurrentBasispointsSettings()
		s.Enabled = false
	} else if validation := s.Validate(); validation != nil {
		err = validation
		s.Enabled = false
	}
	SetBasispointsSettings(s)
	if publishEnabled != nil {
		publishEnabled(s.Enabled)
	}
	return err
}
func StartBasispointsSettingsPoller(parent context.Context, db *database.DB, publishEnabled func(bool)) bool {
	if db == nil {
		return false
	}
	return db.RunBackgroundTask(func(lifecycle context.Context) {
		ctx, cancel := context.WithCancel(lifecycle)
		defer cancel()
		stop := context.AfterFunc(parent, cancel)
		defer stop()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = SyncBasispointsSettings(ctx, db, publishEnabled)
			}
		}
	})
}
