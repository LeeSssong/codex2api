package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"github.com/codex2api/internal/basispoints"
	"github.com/tidwall/gjson"
)

const codexHistoryNamespace = "codex-route-history-v1"
const codexHistoryTTL = 24 * time.Hour
const codexHistoryMaxKeys = 1024

// History identity, rather than a shared session header, pins the upstream.
// Subagents can share session headers while carrying unrelated conversations.
// Only owner-scoped digests and a path are stored; no content or credentials.
func codexHistoryKeys(body []byte, output bool) []string {
	keys := make([]string, 0)
	seen := make(map[string]bool)
	add := func(kind, value string) {
		if value == "" {
			return
		}
		key := compactionContentDigest(kind + ":" + value)
		if !seen[key] && len(keys) <= codexHistoryMaxKeys {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	inspect := func(item gjson.Result) {
		add("encrypted", item.Get("encrypted_content").String())
		for _, part := range item.Get("content").Array() {
			if part.Get("type").String() == "encrypted_content" {
				add("encrypted", part.Get("encrypted_content").String())
			}
		}
		if output || item.Get("type").String() == "item_reference" {
			add("item", item.Get("id").String())
		}
		if output {
			add("call", item.Get("call_id").String())
		}
	}
	if output {
		root := gjson.ParseBytes(body)
		inspect(root.Get("item"))
		for _, path := range []string{"output", "response.output"} {
			for _, item := range root.Get(path).Array() {
				inspect(item)
			}
		}
		add("response", root.Get("response.id").String())
		if root.Get("object").String() == "response.compaction" {
			add("response", root.Get("id").String())
		}
	} else {
		add("response", gjson.GetBytes(body, "previous_response_id").String())
		pending := make(map[string]string)
		for _, item := range gjson.GetBytes(body, "input").Array() {
			inspect(item)
			kind, id := item.Get("type").String(), item.Get("call_id").String()
			switch kind {
			case "function_call", "custom_tool_call":
				pending[id] = kind
			case "function_call_output", "custom_tool_call_output":
				if pending[id] == strings.TrimSuffix(kind, "_output") {
					delete(pending, id)
				} else {
					add("call", id)
				}
			}
		}
		// Complete plaintext tool pairs are portable, even when older calls
		// were produced by a different path before a safe switch.
		for id := range pending {
			add("call", id)
		}
	}
	return keys
}

func hasNestedCodexEncryptedContent(item gjson.Result) bool {
	for _, part := range item.Get("content").Array() {
		if part.Get("type").String() == "encrypted_content" {
			return true
		}
	}
	return false
}

func (d *CodexRouteDecision) hasKnownHistoryRoute() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.HistoryLockReason == "history_provenance" || d.historyError != nil
}

func (d *CodexRouteDecision) bindHistory(body []byte) {
	unsafe := codexHistoryReplayError(body)
	if unsafe == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.validateHistoryProtocolLocked(body)
	d.NoSwitch = true
	if d.HistoryLockReason == "" {
		d.HistoryLockReason = "history_not_replayable"
		d.pinnedPath = d.Preferred
		if d.FinalPath != "" {
			d.pinnedPath = d.FinalPath
		}
	}
	if d.historyError != nil || d.historyCache == nil {
		return
	}
	keys := codexHistoryKeys(body, false)
	if len(keys) > codexHistoryMaxKeys {
		d.historyError = routeLocalError("codex_route_history_too_large", "Too many opaque history references to validate the upstream route")
		d.historyError.HTTPStatus = http.StatusBadRequest
		return
	}
	values, err := d.readHistoryRoutes(keys)
	if err != nil {
		// Retain the preferred path on lookup failure without assuming replayability.
		log.Printf("[CodexRoute] history_lookup_failed error_kind=%s switch_blocked=history_not_replayable", codexHistoryCacheErrorKind(err))
		return
	}
	known := ""
	for _, key := range keys {
		raw, found := values[d.historyOwner+":"+key]
		if !found {
			continue
		}
		var path string
		if json.Unmarshal(raw, &path) != nil || path != database.CodexPathNative && path != database.CodexPathBasispoints {
			d.historyError = routeLocalError("codex_route_history_unavailable", "History route metadata is invalid")
			return
		}
		if known != "" && known != path {
			d.historyError = routeLocalError("codex_route_history_conflict", "History contains opaque state from different upstreams; start a new conversation or supply complete plaintext history")
			d.historyError.HTTPStatus = http.StatusBadRequest
			return
		}
		known = path
	}
	if known == "" {
		return
	}
	allowed := false
	for _, path := range d.Paths {
		allowed = allowed || path == known
	}
	if !allowed || d.FinalPath != "" && d.FinalPath != known {
		d.historyError = routeLocalError("codex_route_history_policy_conflict", "The upstream that created this history is not permitted by the current route; restore that route or start a new conversation")
		return
	}
	d.pinnedPath = known
	d.HistoryLockReason = "history_provenance"
}

func (d *CodexRouteDecision) validateHistoryProtocolLocked(body []byte) {
	if d.historyError != nil || d.pinnedPath != database.CodexPathBasispoints {
		return
	}
	if reason := basispoints.NativeCodexReason(body, basispointsImageHostAvailable()); reason != "" {
		d.historyError = routeLocalError("codex_route_history_protocol_conflict", "This history is pinned to Basispoints, but the request requires native Codex ("+reason+"). Remove the incompatible feature or start a new conversation on native Codex; retrying the same history cannot resolve this conflict.")
		d.historyError.HTTPStatus = http.StatusBadRequest
	}
}

func (a *codexRouteAttemptState) recordHistoryRoute(payload []byte, seen map[string]bool) {
	d := a.decision
	if d.historyCache == nil {
		return
	}
	raw, _ := json.Marshal(a.path)
	values := make(map[string]json.RawMessage)
	for _, key := range codexHistoryKeys(payload, true) {
		if seen[key] || len(seen)+len(values) >= codexHistoryMaxKeys {
			continue
		}
		values[d.historyOwner+":"+key] = raw
	}
	if len(values) == 0 {
		return
	}
	// These output identities have already been produced. Client cancellation
	// must not erase their provenance while a drained stream is being observed.
	ctx := context.WithoutCancel(d.client)
	var err error
	if writer, ok := d.historyCache.(cache.RuntimeBatchWriter); ok {
		err = writer.SetRuntimeBatch(ctx, codexHistoryNamespace, values, codexHistoryTTL)
	} else {
		for key, value := range values {
			if err = d.historyCache.SetRuntime(ctx, codexHistoryNamespace, key, value, codexHistoryTTL); err != nil {
				break
			}
		}
	}
	if err != nil {
		log.Printf("[CodexRoute] history_record_failed path=%s account=%d error_kind=%s", a.path, a.account.ID(), codexHistoryCacheErrorKind(err))
		return
	}
	// A failed write remains retryable when the completion repeats the item.
	for key := range values {
		seen[strings.TrimPrefix(key, d.historyOwner+":")] = true
	}
}

func (d *CodexRouteDecision) readHistoryRoutes(keys []string) (map[string]json.RawMessage, error) {
	scoped := make([]string, 0, len(keys))
	for _, key := range keys {
		scoped = append(scoped, d.historyOwner+":"+key)
	}
	if reader, ok := d.historyCache.(cache.RuntimeBatchReader); ok {
		return reader.GetRuntimeBatch(d.client, codexHistoryNamespace, scoped)
	}
	values := make(map[string]json.RawMessage)
	for _, key := range scoped {
		raw, found, err := d.historyCache.GetRuntime(d.client, codexHistoryNamespace, key)
		if err != nil {
			return nil, err
		}
		if found {
			values[key] = raw
		}
	}
	return values, nil
}

func codexHistoryCacheErrorKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return "network"
	}
	return "cache_backend"
}

func codexRouteSelectionError(err error) bool {
	e, ok := err.(*Error)
	return ok && strings.HasPrefix(e.Code, "codex_route_")
}
