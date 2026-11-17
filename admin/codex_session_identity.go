package admin

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

// officialCodexAccountRow reports accounts whose egress is the official Codex
// path. Blank upstream_type is a historical Codex account. Relay, Grok,
// Antigravity, and Claude accounts are left alone.
func officialCodexAccountRow(row *database.AccountRow) bool {
	if row == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(row.GetCredential("upstream_type"))) {
	case auth.UpstreamGrok, auth.UpstreamAntigravity, auth.UpstreamClaude, auth.UpstreamOpenAIResponses:
		return false
	default:
		return true
	}
}

func resultingFingerprintMode(row *database.AccountRow, update accountSchedulerUpdate) string {
	mode := ""
	if row != nil {
		mode = row.GetCredential(auth.CodexFingerprintModeCredentialKey)
	}
	if update.CodexFingerprintMode.Set {
		mode = update.CodexFingerprintMode.Value
	}
	return auth.NormalizeCodexFingerprintMode(mode)
}

func resultingCustomHeaders(row *database.AccountRow, update accountSchedulerUpdate) map[string]string {
	if update.CustomHeaders.Set {
		return update.CustomHeaders.Values
	}
	if row == nil {
		return nil
	}
	return row.GetCredentialStringMap("custom_headers")
}

type singleMachineHeaderConflict struct {
	accountIDs []int64
	headers    []string
}

func (e *singleMachineHeaderConflict) Error() string {
	names := append([]string(nil), e.headers...)
	sort.Strings(names)
	prefix := "会话身份收敛不能保存身份类自定义请求头"
	if len(e.accountIDs) > 0 {
		ids := make([]string, len(e.accountIDs))
		for i, id := range e.accountIDs {
			ids[i] = strconv.FormatInt(id, 10)
		}
		prefix += "（账号 " + strings.Join(ids, ", ") + "）"
	}
	return prefix + ": " + strings.Join(names, ", ") + "。请先删除这些键；安装标识使用账号自己的收敛值"
}

// singleMachineIdentityHeaderConflict rejects a save that would leave an
// official Codex account in session-identity mode while custom headers still
// name the device or session. Non-identity headers stay allowed.
func singleMachineIdentityHeaderConflict(rows []*database.AccountRow, update accountSchedulerUpdate) error {
	if !update.CodexFingerprintMode.Set && !update.CustomHeaders.Set {
		return nil
	}
	var ids []int64
	seen := map[string]struct{}{}
	var names []string
	for _, row := range rows {
		if !officialCodexAccountRow(row) {
			continue
		}
		if resultingFingerprintMode(row, update) != auth.CodexFingerprintModeSingleMachineMultiWindow {
			continue
		}
		blocked := proxy.CodexSingleMachineBlockedCustomHeaders(resultingCustomHeaders(row, update))
		if len(blocked) == 0 {
			continue
		}
		ids = append(ids, row.ID)
		for _, name := range blocked {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return &singleMachineHeaderConflict{accountIDs: ids, headers: names}
}

// MigrateCodexSessionIdentity moves official Codex accounts that are still off
// to session-identity convergence. It is an explicit settings action and is
// never run on startup. Accounts whose custom headers still carry identity
// fields are skipped so those headers are not silently stripped.
func (h *Handler) MigrateCodexSessionIdentity(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	rows, err := h.db.ListActive(ctx)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "查询账号失败: "+err.Error())
		return
	}
	ids := make([]int64, 0, len(rows))
	skipped := 0
	for _, row := range rows {
		if !officialCodexAccountRow(row) {
			continue
		}
		if auth.NormalizeCodexFingerprintMode(row.GetCredential(auth.CodexFingerprintModeCredentialKey)) != auth.CodexFingerprintModeOff {
			continue
		}
		if len(proxy.CodexSingleMachineBlockedCustomHeaders(row.GetCredentialStringMap("custom_headers"))) > 0 {
			skipped++
			continue
		}
		ids = append(ids, row.ID)
	}
	updated := 0
	if len(ids) > 0 {
		updatedIDs, err := h.db.BatchUpdateAccountMetadata(ctx, ids, database.BatchAccountMetadataUpdate{
			CredentialUpdates: map[string]interface{}{
				auth.CodexFingerprintModeCredentialKey: auth.CodexFingerprintModeSingleMachineMultiWindow,
			},
		})
		if err != nil {
			writeError(c, http.StatusInternalServerError, "更新账号指纹档位失败: "+err.Error())
			return
		}
		updated = len(updatedIDs)
		if h.store != nil {
			for _, id := range updatedIDs {
				h.store.ApplyAccountCodexFingerprintMode(id, auth.CodexFingerprintModeSingleMachineMultiWindow)
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"updated":          updated,
		"skipped_conflict": skipped,
		"message":          "已将关闭的官方账号改为会话身份收敛",
	})
}
