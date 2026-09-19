package turnstate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/cache"
)

const (
	storeNamespace = "codex_turn_state_reuse"
	storeLeaseTTL  = 5 * time.Second
)

type Key struct {
	AccountID      int64
	Model          string
	CredentialHash string
}

func (k Key) String() string {
	return fmt.Sprintf("%d:%s:%s", k.AccountID, strings.TrimSpace(k.Model), strings.TrimSpace(k.CredentialHash))
}

func (k Key) valid() bool {
	return k.AccountID > 0 && strings.TrimSpace(k.Model) != "" && strings.TrimSpace(k.CredentialHash) != ""
}

type storedTicket struct {
	Raw      string `json:"raw"`
	IssuedAt int64  `json:"issued_at"`
}

type Store struct {
	backend cache.TokenCache
	mu      sync.RWMutex
	mirror  map[string]Ticket
}

func NewStore(backend cache.TokenCache) *Store {
	return &Store{backend: backend, mirror: make(map[string]Ticket)}
}

func (s *Store) Get(ctx context.Context, key Key, now time.Time) (Ticket, bool, error) {
	if s == nil || s.backend == nil || !key.valid() {
		return Ticket{}, false, nil
	}
	raw, ok, err := s.backend.GetRuntime(ctx, storeNamespace, key.String())
	if err != nil || !ok {
		return Ticket{}, false, err
	}
	var record storedTicket
	if err := json.Unmarshal(raw, &record); err != nil {
		return Ticket{}, false, err
	}
	ticket, err := Parse(record.Raw, now)
	if err != nil || ticket.IssuedAt.Unix() != record.IssuedAt {
		_ = s.backend.DeleteRuntime(ctx, storeNamespace, key.String())
		s.deleteMirror(key.String())
		return Ticket{}, false, nil
	}
	s.setMirror(key.String(), ticket)
	return ticket, true, nil
}

func (s *Store) Put(ctx context.Context, key Key, ticket Ticket) (bool, error) {
	if s == nil || s.backend == nil {
		return false, errors.New("turn-state store is unavailable")
	}
	if !key.valid() || ticket.Raw == "" || ticket.IssuedAt.IsZero() || ticket.ExpiresAt.IsZero() {
		return false, errors.New("invalid turn-state store entry")
	}
	return s.withLease(ctx, key, func() (bool, error) {
		raw, ok, err := s.backend.GetRuntime(ctx, storeNamespace, key.String())
		if err != nil {
			return false, err
		}
		if ok {
			var current storedTicket
			if json.Unmarshal(raw, &current) == nil && current.IssuedAt >= ticket.IssuedAt.Unix() {
				return false, nil
			}
		}
		record, err := json.Marshal(storedTicket{Raw: ticket.Raw, IssuedAt: ticket.IssuedAt.Unix()})
		if err != nil {
			return false, err
		}
		ttl := time.Until(ticket.ExpiresAt)
		if ttl <= 0 {
			return false, nil
		}
		if err := s.backend.SetRuntime(ctx, storeNamespace, key.String(), record, ttl); err != nil {
			return false, err
		}
		s.setMirror(key.String(), ticket)
		return true, nil
	})
}

func (s *Store) DeleteIfMatch(ctx context.Context, key Key, rawTicket string) (bool, error) {
	if s == nil || s.backend == nil || !key.valid() || rawTicket == "" {
		return false, nil
	}
	return s.withLease(ctx, key, func() (bool, error) {
		raw, ok, err := s.backend.GetRuntime(ctx, storeNamespace, key.String())
		if err != nil || !ok {
			return false, err
		}
		var current storedTicket
		if err := json.Unmarshal(raw, &current); err != nil {
			return false, err
		}
		if current.Raw != rawTicket {
			return false, nil
		}
		if err := s.backend.DeleteRuntime(ctx, storeNamespace, key.String()); err != nil {
			return false, err
		}
		s.deleteMirror(key.String())
		return true, nil
	})
}

func (s *Store) withLease(ctx context.Context, key Key, fn func() (bool, error)) (bool, error) {
	ownerBytes := make([]byte, 16)
	if _, err := rand.Read(ownerBytes); err != nil {
		return false, err
	}
	owner := hex.EncodeToString(ownerBytes)
	leaseKey := "ticket:" + key.String()
	deadline := time.NewTimer(storeLeaseTTL)
	defer deadline.Stop()
	for {
		acquired, err := s.backend.AcquireLease(ctx, storeNamespace, leaseKey, owner, storeLeaseTTL)
		if err != nil {
			return false, err
		}
		if acquired {
			defer func() { _ = s.backend.ReleaseLease(context.Background(), storeNamespace, leaseKey, owner) }()
			return fn()
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return false, errors.New("timed out acquiring turn-state store lease")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (s *Store) setMirror(key string, ticket Ticket) {
	s.mu.Lock()
	s.mirror[key] = ticket
	s.mu.Unlock()
}

func (s *Store) deleteMirror(key string) {
	s.mu.Lock()
	delete(s.mirror, key)
	s.mu.Unlock()
}
