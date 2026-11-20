package auth

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

// ensureWorkstations 给还没有工作站记录的账号补一份当前身份快照。
// 已有记录不改。快照只描述现在对外使用的安装标识、设备和出口，不换指纹档位。
func (s *Store) ensureWorkstations(ctx context.Context) {
	if s == nil || s.db == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.RLock()
	accounts := append([]*Account(nil), s.accounts...)
	s.mu.RUnlock()
	for _, acc := range accounts {
		s.ensureAccountWorkstation(ctx, acc)
	}
}

func (s *Store) ensureAccountWorkstation(ctx context.Context, acc *Account) {
	if s == nil || s.db == nil || acc == nil || acc.DBID == 0 {
		return
	}
	row, err := s.db.GetAccountByID(ctx, acc.DBID)
	if err != nil || row == nil {
		if err != nil {
			log.Printf("[账号 %d] 读取工作站记录失败: %v", acc.DBID, err)
		}
		return
	}
	if strings.TrimSpace(row.GetCredential(WorkstationCredentialKey)) != "" {
		return
	}
	ws := acc.WorkstationSnapshot()
	raw, err := json.Marshal(ws)
	if err != nil {
		return
	}
	if err := s.db.UpdateCredentials(ctx, acc.DBID, map[string]interface{}{WorkstationCredentialKey: string(raw)}); err != nil {
		log.Printf("[账号 %d] 保存工作站记录失败: %v", acc.DBID, err)
		return
	}
	acc.RememberWorkstation(ws)
}

// HoldWorkstation 停止这个账号继续放量。
// 只写运行时错误态，不改 accounts.enabled，管理员仍可手动恢复。
func (s *Store) HoldWorkstation(acc *Account, reason string) {
	if s == nil || acc == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = WorkstationHoldReason
	}
	acc.mu.Lock()
	already := acc.Status == StatusError && acc.ErrorMsg == reason
	acc.mu.Unlock()
	if already {
		return
	}
	log.Printf("[账号 %d] 工作站对账失败，停止放量: %s", acc.DBID, reason)
	s.MarkError(acc, reason)
	atomic.StoreInt32(&acc.DispatchPaused, 1)
	s.fastSchedulerUpdate(acc)
}

// WorkstationVersionDue 报告工作站账号现在是否该采用新的客户端版本。
// 没有记录的账号先保持当前版本。升级窗口按账号摊到 6 小时，避免整池同一秒换版本。
func WorkstationVersionDue(ws Workstation, accountID int64, now time.Time) bool {
	if ws.Schema == 0 || strings.TrimSpace(ws.Version) == "" || now.IsZero() {
		return false
	}
	adopted := time.Unix(ws.VersionAdoptedAtUnix, 0)
	if ws.VersionAdoptedAtUnix <= 0 {
		adopted = time.Unix(0, 0)
	}
	slot := time.Duration(accountID%21600) * time.Second
	if slot < 0 {
		slot = -slot
	}
	return !now.Before(adopted.Add(6*time.Hour + slot))
}
