package proxy

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/database"
)

// 官方 daily usage 的 client_id 和出站画像形态不是同一套名字。
// 这里只保留已经在日用量里出现过、并且目录里有对应画像的入口。
// CODEX_UNKNOWN_DEFAULT 是走本网关、上游认不出客户端的流量，不能拿来钉画像。
var codexReportedClientKinds = map[string]CodexClientKind{
	"CODEX_CLI":          CodexClientKindTUI,
	"CODEX_DESKTOP_APP":  CodexClientKindDesktop,
	"CODEX_WORK_DESKTOP": CodexClientKindDesktop,
	"CODEX_IDE_VSCODE":   CodexClientKindVSCode,
	"CODEX_SERVICE_EXEC": CodexClientKindExec,
}

type codexReportedClientSource interface {
	ListAccountDailyUsage(ctx context.Context, accountID int64, days int) ([]*database.AccountDailyUsage, error)
}

type codexReportedClientCacheEntry struct {
	kind   CodexClientKind
	loaded bool
	at     time.Time
}

var (
	codexReportedClientMu     sync.RWMutex
	codexReportedClientDB     codexReportedClientSource
	codexReportedClientCached = map[int64]codexReportedClientCacheEntry{}
)

const (
	codexReportedClientLookbackDays = 90
	codexReportedClientCacheTTL     = 10 * time.Minute
)

// SetCodexReportedClientSource 接入账号日用量。画像生成在请求路径上，
// 查库失败或没有记录时保持原来的号池抽样，不能把请求打失败。
func SetCodexReportedClientSource(db codexReportedClientSource) {
	codexReportedClientMu.Lock()
	codexReportedClientDB = db
	codexReportedClientCached = map[int64]codexReportedClientCacheEntry{}
	codexReportedClientMu.Unlock()
}

type codexReportedClientRow struct {
	ClientID string  `json:"client_id"`
	Users    int     `json:"users"`
	Threads  int     `json:"threads"`
	Turns    int     `json:"turns"`
	Credits  float64 `json:"credits"`
	Tokens   *int64  `json:"text_total_tokens"`
}

// codexReportedClientKind 返回这个号在官方日用量里最近一次真正用过的客户端。
// 同一天出现多种客户端时，优先取有实际 users、线程或轮次的那一种。
// 未结算的空壳（这三项都是 0）不能覆盖更早已经确认过的客户端；
// 整段历史都只有 token 时，仍按 token 选出客户端。
func codexReportedClientKind(accountID int64) (CodexClientKind, bool) {
	if accountID <= 0 {
		return "", false
	}
	codexReportedClientMu.RLock()
	entry, ok := codexReportedClientCached[accountID]
	db := codexReportedClientDB
	codexReportedClientMu.RUnlock()
	if ok && time.Since(entry.at) < codexReportedClientCacheTTL {
		return entry.kind, entry.loaded && entry.kind != ""
	}
	kind := CodexClientKind("")
	loaded := false
	if db != nil {
		rows, err := db.ListAccountDailyUsage(context.Background(), accountID, codexReportedClientLookbackDays)
		if err != nil {
			// 查库失败不写入缓存，下一次请求再试。画像继续走号池配比。
			return "", false
		}
		loaded = true
		kind = newestReportedCodexClient(rows)
	}
	codexReportedClientMu.Lock()
	codexReportedClientCached[accountID] = codexReportedClientCacheEntry{kind: kind, loaded: loaded, at: time.Now()}
	codexReportedClientMu.Unlock()
	return kind, loaded && kind != ""
}

func newestReportedCodexClient(rows []*database.AccountDailyUsage) CodexClientKind {
	var engagedDay, anyDay string
	var engagedScore, anyScore int64
	var engaged, any CodexClientKind
	for _, row := range rows {
		if row == nil {
			continue
		}
		day := strings.TrimSpace(row.Day)
		kind, score, used := dominantReportedCodexClient(row.ClientsJSON)
		if kind == "" || score <= 0 {
			continue
		}
		if day > anyDay || (day == anyDay && score > anyScore) {
			anyDay, anyScore, any = day, score, kind
		}
		// 未结算日常常把 users/threads/turns 清成 0。这种空壳不能把已经
		// 确认过的客户端换成另一种，等官方给出实际使用再切换。
		if !used {
			continue
		}
		if day > engagedDay || (day == engagedDay && score > engagedScore) {
			engagedDay, engagedScore, engaged = day, score, kind
		}
	}
	if engaged != "" {
		return engaged
	}
	return any
}

func dominantReportedCodexClient(raw string) (CodexClientKind, int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" || raw == "null" {
		return "", 0, false
	}
	var rows []codexReportedClientRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return "", 0, false
	}
	var engagedKind, anyKind CodexClientKind
	var engagedScore, anyScore int64
	for _, row := range rows {
		kind, ok := codexReportedClientKinds[strings.ToUpper(strings.TrimSpace(row.ClientID))]
		if !ok {
			continue
		}
		score, used := reportedClientScore(row)
		if score > anyScore {
			anyKind, anyScore = kind, score
		}
		if used && score > engagedScore {
			engagedKind, engagedScore = kind, score
		}
	}
	if engagedKind != "" {
		return engagedKind, engagedScore, true
	}
	return anyKind, anyScore, false
}

func reportedClientScore(row codexReportedClientRow) (int64, bool) {
	used := row.Users > 0 || row.Threads > 0 || row.Turns > 0
	score := int64(row.Users)*1_000_000_000 + int64(row.Threads)*1_000_000 + int64(row.Turns)*1_000
	if row.Tokens != nil && *row.Tokens > 0 {
		score += *row.Tokens
	} else if row.Credits > 0 {
		score += int64(row.Credits * 1000)
	}
	return score, used
}
