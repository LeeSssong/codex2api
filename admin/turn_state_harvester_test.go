package admin

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"github.com/codex2api/internal/turnstate"
)

func harvestTestTicket(decodedLen int, issued time.Time) string {
	raw := make([]byte, decodedLen)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(issued.Unix()))
	return base64.URLEncoding.EncodeToString(raw)
}

func TestHarvestTurnStateTicketRequiresTwoQualifiedCalls(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	first := harvestTestTicket(217, now.Add(-time.Minute))
	second := harvestTestTicket(249, now)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Chatgpt-Account-Id"); got != "acct-1" {
			t.Errorf("Chatgpt-Account-Id = %q", got)
		}
		if call == 1 {
			if got := r.Header.Get(turnstate.HeaderName); got != "" {
				t.Errorf("first call turn-state = %q", got)
			}
			w.Header().Set(turnstate.HeaderName, first)
		} else {
			if got := r.Header.Get(turnstate.HeaderName); got != first {
				t.Errorf("second call turn-state = %q, want first ticket", got)
			}
			w.Header().Set(turnstate.HeaderName, second)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"type":"response.completed","response":{"model":"gpt-6-astra","status":"completed"}}`)
	}))
	defer server.Close()

	h := &Handler{
		turnStateHarvestEndpoint: server.URL,
		turnStateHarvestClient:   func(string) *http.Client { return server.Client() },
	}
	account := &auth.Account{DBID: 7, AccessToken: "access-token", AccountID: "acct-1"}
	result, err := h.harvestTurnStateTicket(context.Background(), account, "http://proxy.invalid", now)
	if err != nil {
		t.Fatalf("harvestTurnStateTicket: %v", err)
	}
	if calls.Load() != 2 || result.Ticket.Raw != second || result.StatusCode != http.StatusOK {
		t.Fatalf("result = %#v calls=%d", result, calls.Load())
	}
}

func TestHarvestTurnStateCallClassifiesIncompleteAndBackoff(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	t.Run("incomplete", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(turnstate.HeaderName, harvestTestTicket(217, now))
			fmt.Fprintln(w, `data: {"type":"response.created","response":{"model":"gpt-6-astra"}}`)
		}))
		defer server.Close()
		h := &Handler{turnStateHarvestEndpoint: server.URL, turnStateHarvestClient: func(string) *http.Client { return server.Client() }}
		_, _, err := h.harvestTurnStateCall(context.Background(), &auth.Account{DBID: 1, AccessToken: "at", AccountID: "acct"}, "", "", now)
		if err == nil {
			t.Fatal("incomplete stream succeeded")
		}
	})

	t.Run("429", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "420")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()
		h := &Handler{turnStateHarvestEndpoint: server.URL, turnStateHarvestClient: func(string) *http.Client { return server.Client() }}
		_, result, err := h.harvestTurnStateCall(context.Background(), &auth.Account{DBID: 1, AccessToken: "at", AccountID: "acct"}, "", "", now)
		if err != nil || result.StatusCode != http.StatusTooManyRequests || result.RetryAfter != 420*time.Second {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
}

func TestHarvestTurnStateAccountKeepsStoredTicketOnAuthFailure(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	existing, err := turnstate.Parse(harvestTestTicket(217, now.Add(-time.Minute)), now)
	if err != nil {
		t.Fatalf("parse existing ticket: %v", err)
	}
	account := &auth.Account{DBID: 7, AccessToken: "access-token", AccountID: "acct-1"}
	key := turnstate.Key{AccountID: account.ID(), Model: turnstate.HarvestModel, CredentialHash: turnstate.CredentialHash(account.AccessToken, account.AccountID)}
	store := turnstate.NewStore(cache.NewMemory(1))
	if published, putErr := store.Put(context.Background(), key, existing); putErr != nil || !published {
		t.Fatalf("seed existing ticket: published=%v err=%v", published, putErr)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	h := &Handler{
		turnStateHarvestEndpoint: server.URL,
		turnStateHarvestClient:   func(string) *http.Client { return server.Client() },
		turnStateHarvestStates:   make(map[string]*turnStateHarvestAccountState),
	}
	target := turnStateHarvestTarget{account: account, key: key, stateID: key.String()}
	h.harvestTurnStateAccount(context.Background(), store, target, true, "", database.TurnStateReuseSettings{}, now)

	got, found, getErr := store.Get(context.Background(), key, now)
	if getErr != nil || !found || got.Raw != existing.Raw {
		t.Fatalf("stored ticket after auth failure: found=%v got=%q err=%v", found, got.Raw, getErr)
	}
}
