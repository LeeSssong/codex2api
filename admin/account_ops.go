package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/codex2api/accountops"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type accountOpsRuntime struct {
	alerts   *accountops.AccountOpsService
	enabled  atomic.Bool
	wg       sync.WaitGroup
	configMu sync.Mutex
}

func (h *Handler) StartAccountOps(ctx context.Context) {
	if h.db == nil || h.accountOps != nil {
		return
	}
	r := &accountOpsRuntime{}
	h.accountOps = r
	r.alerts = accountops.NewAccountOpsService(database.NewAccountOpsSettings(h.db), database.NewAccountOpsRepository(h.db), accountops.SMTPSender{Config: h.db.GetAccountOpsSMTP})
	r.alerts.SetModuleGate(r.enabled.Load)
	h.refreshAccountOpsModule(ctx)
	r.alerts.Start()
	if r.enabled.Load() {
		proxy.SetAccountOpsObserver(r.alerts)
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				proxy.SetAccountOpsObserver(nil)
				r.alerts.Stop()
				return
			case <-ticker.C:
				if e := h.db.CleanupAccountQualityHistory(ctx); e != nil {
					log.Printf("[account-ops] history cleanup: %v", e)
				}
				h.refreshAccountOpsModule(ctx)
				if !r.enabled.Load() {
					continue
				}
				for i := 0; i < database.QualityTestConcurrency; i++ {
					plan, e := h.db.ClaimAccountQualityPlan(ctx, time.Now().UTC())
					if e != nil {
						log.Printf("[account-ops] plan claim failed: %v", e)
						break
					}
					if plan == nil {
						break
					}
					r.wg.Add(1)
					go func(p accountops.Plan) { defer r.wg.Done(); h.runAccountQualityRound(ctx, p) }(*plan)
				}
			}
		}
	}()
}
func (h *Handler) WaitAccountOps() {
	if h.accountOps != nil {
		h.accountOps.wg.Wait()
	}
}
func (h *Handler) refreshAccountOpsModule(ctx context.Context) {
	raw, e := database.NewAccountOpsSettings(h.db).GetValue(ctx, "module_enabled")
	enabled := e == nil && raw == "true"
	h.accountOps.enabled.Store(enabled)
	if enabled {
		proxy.SetAccountOpsObserver(h.accountOps.alerts)
	} else {
		proxy.SetAccountOpsObserver(nil)
	}
}
func (h *Handler) requireAccountOps(c *gin.Context) bool {
	if h.accountOps == nil || !h.accountOps.enabled.Load() {
		c.JSON(http.StatusConflict, gin.H{"error": "请先启用账号运维内置模块"})
		return false
	}
	return true
}
func (h *Handler) GetAccountOpsModule(c *gin.Context) {
	enabled := h.accountOps != nil && h.accountOps.enabled.Load()
	c.JSON(200, gin.H{"enabled": enabled})
}
func (h *Handler) SaveAccountOpsModule(c *gin.Context) {
	if h.accountOps == nil {
		c.JSON(503, gin.H{"error": "服务不可用"})
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if e := c.ShouldBindJSON(&req); e != nil {
		c.JSON(400, gin.H{"error": "无效配置"})
		return
	}
	if e := database.NewAccountOpsSettings(h.db).Set(c.Request.Context(), "module_enabled", strconv.FormatBool(req.Enabled)); e != nil {
		c.JSON(500, gin.H{"error": "保存失败"})
		return
	}
	h.refreshAccountOpsModule(c.Request.Context())
	if !req.Enabled {
		cfg, e := h.accountOps.alerts.GetConfig(c.Request.Context())
		if e == nil {
			cfg.Enabled = false
			_ = h.accountOps.alerts.SaveConfig(c.Request.Context(), cfg)
		}
	}
	h.GetAccountOpsModule(c)
}
func (h *Handler) GetAccountOpsConfig(c *gin.Context) {
	if h.accountOps == nil {
		c.JSON(503, gin.H{"error": "服务不可用"})
		return
	}
	cfg, e := h.accountOps.alerts.GetConfig(c.Request.Context())
	if e != nil {
		c.JSON(500, gin.H{"error": "读取失败"})
		return
	}
	smtp, e := h.db.GetAccountOpsSMTP(c.Request.Context())
	if e != nil {
		c.JSON(500, gin.H{"error": "读取邮件配置失败"})
		return
	}
	smtp.Password = ""
	dropped, failed := h.accountOps.alerts.RuntimeCounters()
	c.JSON(200, gin.H{"config": cfg, "smtp": smtp, "dropped": dropped, "failures": failed})
}
func (h *Handler) SaveAccountOpsConfig(c *gin.Context) {
	if !h.requireAccountOps(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32*1024)
	var req struct {
		Config accountops.AccountOpsConfig `json:"config"`
		SMTP   accountops.SMTPConfig       `json:"smtp"`
	}
	if e := c.ShouldBindJSON(&req); e != nil {
		c.JSON(400, gin.H{"error": "无效配置"})
		return
	}
	if e := accountops.ValidateAccountOpsConfig(req.Config); e != nil {
		c.JSON(400, gin.H{"error": e.Error()})
		return
	}
	if e := req.SMTP.Validate(); e != nil {
		c.JSON(400, gin.H{"error": e.Error()})
		return
	}
	if req.Config.Enabled && req.SMTP.Host == "" {
		c.JSON(400, gin.H{"error": "启用告警前请配置 SMTP"})
		return
	}
	h.accountOps.configMu.Lock()
	defer h.accountOps.configMu.Unlock()
	if e := h.db.SaveAccountOpsSMTP(c.Request.Context(), req.SMTP); e != nil {
		c.JSON(500, gin.H{"error": "保存邮件配置失败"})
		return
	}
	if e := h.accountOps.alerts.SaveConfig(c.Request.Context(), req.Config); e != nil {
		c.JSON(500, gin.H{"error": "保存告警配置失败"})
		return
	}
	h.GetAccountOpsConfig(c)
}
func (h *Handler) ListAccountOpsAlerts(c *gin.Context) {
	offset, _ := strconv.Atoi(c.Query("offset"))
	items, e := database.NewAccountOpsRepository(h.db).List(c.Request.Context(), offset, 100)
	if e != nil {
		c.JSON(500, gin.H{"error": "读取失败"})
		return
	}
	c.JSON(200, gin.H{"items": items})
}
func (h *Handler) ListAccountQualityPlans(c *gin.Context) {
	items, e := h.db.ListAccountQualityPlans(c.Request.Context())
	if e != nil {
		c.JSON(500, gin.H{"error": "读取规则失败"})
		return
	}
	c.JSON(200, gin.H{"items": items})
}
func (h *Handler) SaveAccountQualityPlan(c *gin.Context) {
	if !h.requireAccountOps(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128*1024)
	var p accountops.Plan
	if e := c.ShouldBindJSON(&p); e != nil {
		c.JSON(400, gin.H{"error": "无效规则"})
		return
	}

	if h.store == nil || h.store.FindByID(p.AccountID) == nil {
		c.JSON(400, gin.H{"error": "账号不在运行时池中"})
		return
	}
	if e := h.validateQualityTestForAccount(c.Request.Context(), h.store.FindByID(p.AccountID), qualityTestRequest{Model: p.Model, Prompt: p.Prompt, ReasoningEffort: p.ReasoningEffort}); e != nil {
		c.JSON(400, gin.H{"error": e.Error()})
		return
	}
	result, e := h.db.SaveAccountQualityPlan(c.Request.Context(), p)
	if e != nil {
		c.JSON(400, gin.H{"error": e.Error()})
		return
	}
	c.JSON(200, result)
}
func (h *Handler) DeleteAccountQualityPlan(c *gin.Context) {
	if !h.requireAccountOps(c) {
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		c.JSON(400, gin.H{"error": "无效 ID"})
		return
	}
	if e = h.db.DeleteAccountQualityPlan(c.Request.Context(), id); e != nil {
		c.JSON(500, gin.H{"error": "删除失败"})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (h *Handler) TriggerAccountQualityPlan(c *gin.Context) {
	if !h.requireAccountOps(c) {
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		c.JSON(400, gin.H{"error": "无效 ID"})
		return
	}
	if e = h.db.TriggerAccountQualityPlan(c.Request.Context(), id); e != nil {
		c.JSON(400, gin.H{"error": "规则不存在或已暂停"})
		return
	}
	c.JSON(202, gin.H{"queued": true})
}
func (h *Handler) ListAccountQualityHistory(c *gin.Context) {
	before, _ := strconv.ParseInt(c.Query("before"), 10, 64)
	items, e := h.db.ListAccountQualityHistory(c.Request.Context(), before, false)
	if e != nil {
		c.JSON(500, gin.H{"error": "读取记录失败"})
		return
	}
	var next int64
	if len(items) > 100 {
		items = items[:100]
		next = items[99].ID
	}
	c.JSON(200, gin.H{"items": items, "next_cursor": next})
}
func (h *Handler) GetAccountQualityRound(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	r, e := h.db.GetAccountQualityRound(c.Request.Context(), id)
	if e != nil {
		c.JSON(404, gin.H{"error": "记录不存在"})
		return
	}
	c.JSON(200, r)
}
func (h *Handler) runAccountQualityRound(parent context.Context, p accountops.Plan) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	round := accountops.Round{PlanID: p.ID, AccountID: p.AccountID, Plan: p, StartedAt: time.Now().UTC(), Results: make([]accountops.Sample, p.Samples)}
	var job *database.QualityTestJob
	skipRound := false
	defer func() {
		if recover() != nil {
			round.Outcome = "inconclusive"
			round.Action = "interrupted"
		}
		round.CompletedAt = time.Now().UTC()
		finish, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if skipRound {
			if e := h.db.DeferAccountQualityPlan(finish, p); e != nil {
				log.Printf("[account-ops] deferred plan: %v", e)
			}
			return
		}
		if job != nil {
			job.Status = "completed"
			if round.Outcome == "inconclusive" {
				job.Status = "error"
				job.Error = "本轮结果不确定"
			}
			raw, _ := json.Marshal(round.Results)
			job.Output = string(raw)
			job.DurationMS = round.CompletedAt.Sub(round.StartedAt).Milliseconds()
			if e := h.db.FinishQualityTest(finish, *job); e != nil {
				log.Printf("[account-ops] job finalization: %v", e)
			}
		}
		if e := h.db.FinishAccountQualityRound(finish, p, round); e != nil {
			log.Printf("[account-ops] round finalization: %v", e)
		}
		if e := h.db.CleanupAccountQualityHistory(finish); e != nil {
			log.Printf("[account-ops] history cleanup: %v", e)
		}
	}()
	row, e := h.db.GetAccountByID(ctx, p.AccountID)
	if e != nil || row == nil {
		round.Outcome = "inconclusive"
		round.Action = "account_deleted"
		return
	}
	round.AccountName = row.Name
	job, e = h.db.CreateQualityTestJob(ctx, database.QualityTestJob{AccountID: p.AccountID, AccountName: row.Name, Model: p.Model, ReasoningEffort: p.ReasoningEffort, Prompt: p.Prompt, PresetKind: "builtin", PresetRef: "account-quality", PresetName: "降智运维"})
	if e != nil {
		round.Outcome = "inconclusive"
		round.Action = "capacity_unavailable"
		skipRound = errors.Is(e, database.ErrQualityTestCapacity) || errors.Is(e, database.ErrQualityTestAccountBusy)
		return
	}
	p.JobID = job.ID
	watcherDone := make(chan struct{})
	watcherExited := make(chan struct{})
	go func() {
		defer close(watcherExited)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-watcherDone:
				return
			case <-ticker.C:
				status, e := h.db.QualityTestStatus(ctx, job.ID)
				if e != nil || status != "running" {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { close(watcherDone); <-watcherExited }()
	var wg sync.WaitGroup
	for i := range round.Results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start := time.Now()
			output, err := h.runAccountOpsText(ctx, p.AccountID, p.Model, p.Prompt+"\n\n只输出最终答案，不要解释。", p.ReasoningEffort)
			r := accountops.Sample{Output: output}
			if err != nil {
				r.Error = err.Error()
				r.Verdict = "unknown"
			} else {
				r.Judgment = h.judgeAccountQuality(ctx, p, output)
			}
			r.DurationMS = time.Since(start).Milliseconds()
			round.Results[i] = r
		}(i)
	}
	wg.Wait()
	round.TotalCount = len(round.Results)
	for _, r := range round.Results {
		if r.Verdict == "correct" && r.Error == "" {
			round.PassedCount++
		}
	}
	round.Outcome = accountops.Outcome(round.Results)
	if ctx.Err() != nil {
		round.Outcome = "inconclusive"
		round.Action = "cancelled"
		return
	}
	if parent.Err() != nil || !h.accountOps.enabled.Load() {
		round.Action = "stale_run"
		return
	}
	round.Action, e = h.db.ApplyAccountQualityOutcome(ctx, p, round.Outcome)
	if e != nil {
		round.Action = "action_failed"
		log.Printf("[account-ops] apply plan=%d: %v", p.ID, e)
		return
	}
	if h.store != nil {
		row, e = h.db.GetAccountByID(ctx, p.AccountID)
		if e == nil && row != nil {
			h.store.ApplyAccountEnabled(p.AccountID, row.Enabled)
			groups, e := h.db.GetAccountGroupIDs(ctx, p.AccountID)
			if e == nil {
				h.store.ApplyAccountGroups(p.AccountID, groups)
			}
		}
		h.invalidateAccountSnapshotCaches()
	}
}
func (h *Handler) runAccountOpsText(parent context.Context, id int64, model, prompt, effort string) (string, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var mu sync.Mutex
	var output, message string
	completed := false
	secrets := codexTestSecrets(h.store.FindByID(id))
	writer := &qualityJobWriter{header: make(http.Header), emit: func(e testEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch e.Type {
		case "content":
			if len(output)+len(e.Text) > 1<<20 {
				message = "output_too_large"
				cancel()
				return
			}
			output += e.Text
		case "error":
			message = sanitizeCodexTestText(e.Error, secrets)
		case "test_complete":
			completed = e.Success
		}
	}}
	router := gin.New()
	router.POST("/accounts/:id/test", func(c *gin.Context) {
		h.testConnection(c, &qualityTestRequest{Model: model, Prompt: prompt, ReasoningEffort: effort, TextOnly: true})
	})
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("/accounts/%d/test", id), nil)
	router.ServeHTTP(writer, request)
	if message != "" {
		return output, errors.New(message)
	}
	if ctx.Err() != nil {
		return output, ctx.Err()
	}
	if !completed || strings.TrimSpace(output) == "" {
		return output, errors.New("incomplete_response")
	}
	return output, nil
}
func (h *Handler) judgeAccountQuality(parent context.Context, p accountops.Plan, answer string) accountops.Judgment {
	r := accountops.Judgment{Verdict: "unknown", Reason: "judge_not_configured"}
	if p.Judge == nil {
		return r
	}
	r.GroupID = p.Judge.GroupID
	r.ModelID = p.Judge.ModelID
	if len(answer) > 64000 {
		r.Reason = "judge_input_too_large"
		return r
	}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	ids, e := h.db.ListAccountIDsInGroups(ctx, []int64{p.Judge.GroupID})
	if e != nil {
		r.Reason = "judge_accounts_unavailable"
		return r
	}
	r.Reason = "judge_no_available_account"
	eligible := map[int64]bool{}
	for _, id := range ids {
		if id == p.AccountID {
			continue
		}
		a := h.store.FindByID(id)
		if a != nil && h.validateQualityTestForAccount(ctx, a, qualityTestRequest{Model: p.Judge.ModelID, Prompt: "judge"}) == nil {
			eligible[id] = true
		}
	}
	excluded := map[int64]bool{p.AccountID: true}
	attempts := 0
	for attempts < 3 && ctx.Err() == nil {
		// Native group memberships carry no independent priority/model allowlist;
		// use the account model catalog and native scheduler's priority/slot policy.
		a := h.store.NextExcludingWithFilter(0, excluded, func(a *auth.Account) bool {
			return eligible[a.DBID] && a.InAnyGroup(map[int64]struct{}{p.Judge.GroupID: {}})
		})
		if a == nil {
			break
		}
		id := a.DBID
		excluded[id] = true
		attempts++
		output, e := func() (string, error) {
			defer h.store.Release(a)
			effort := "medium"
			if a.IsAntigravityAPI() {
				effort = ""
			}
			return h.runAccountOpsText(ctx, id, p.Judge.ModelID, accountops.JudgePrompt(p, answer), effort)
		}()
		r.AccountID = id
		if e != nil {
			r.Reason = "judge_request_failed"
		} else {
			judgment, e := accountops.ParseJudgment(output)
			if e == nil {
				judgment.AccountID = id
				judgment.GroupID = p.Judge.GroupID
				judgment.ModelID = p.Judge.ModelID
				return *judgment
			}
			r.Reason = "judge_invalid_response"
		}
		if attempts >= 3 || ctx.Err() != nil {
			break
		}
	}
	return r
}
