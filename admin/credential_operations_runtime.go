package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/codex2api/credentialops"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"os"
	"strconv"
	"sync"
	"time"
)

func (h *Handler) GetCredentialOpsEncryption(c *gin.Context) {
	ok, source := credentialops.EncryptionStatus()
	c.JSON(200, gin.H{"configured": ok, "source": source})
}
func (h *Handler) InitializeCredentialOpsEncryption(c *gin.Context) {
	has, e := h.db.HasCredentialOperationsCiphertext(c.Request.Context())
	if e != nil {
		c.Status(500)
		return
	}
	ok, source, e := credentialops.InitializeEncryption(has)
	if e != nil {
		c.JSON(409, gin.H{"error": e.Error()})
		return
	}
	c.JSON(200, gin.H{"configured": ok, "source": source})
}
func (h *Handler) SaveCredentialOpsRuntime(c *gin.Context) {
	var in struct {
		Enabled               *bool             `json:"enabled"`
		Engine                string            `json:"engine"`
		WorkerConcurrency     int               `json:"worker_concurrency"`
		SessionStudioEndpoint string            `json:"session_studio_endpoint"`
		SessionStudioHeaders  map[string]string `json:"session_studio_headers"`
	}
	if c.ShouldBindJSON(&in) != nil {
		c.Status(400)
		return
	}
	old, _ := h.db.CredentialOpsRuntime(c.Request.Context())
	if in.Enabled != nil {
		old.Enabled = *in.Enabled
	}
	if in.Engine != "" {
		old.Engine = in.Engine
	}
	if in.WorkerConcurrency > 0 {
		old.WorkerConcurrency = in.WorkerConcurrency
	}
	old.SessionStudioEndpoint = in.SessionStudioEndpoint
	old.SessionStudioHeaders = ""
	if len(in.SessionStudioHeaders) > 0 {
		raw, _ := json.Marshal(in.SessionStudioHeaders)
		old.SessionStudioHeaders = string(raw)
	}
	if e := h.db.SaveCredentialOpsRuntime(c.Request.Context(), old); e != nil {
		c.JSON(400, gin.H{"error": e.Error()})
		return
	}
	saved, _ := h.db.CredentialOpsRuntime(c.Request.Context())
	c.JSON(200, saved)
}
func (h *Handler) RetryCredentialOpsLogin(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	task, e := h.db.FindCredentialOpsRetryOutcome(c.Request.Context(), id)
	if e == nil && task == nil {
		var source *database.CredentialOpsTaskRow
		source, e = h.db.GetCredentialOpsTask(c.Request.Context(), id)
		if e == nil && source != nil && source.Kind == "login" && source.Status != "queued" && source.Status != "running" && source.Status != "succeeded" {
			cfg, loadErr := h.db.CredentialOpsTaskConfig(c.Request.Context(), id)
			e = loadErr
			if e == nil && source.AccountID > 0 {
				cfg, e = h.db.GetCredentialOpsLoginConfig(c.Request.Context(), source.AccountID)
			}
			if e == nil && cfg == nil {
				e = errors.New("saved login configuration unavailable")
			}
			if e == nil {
				var resolved string
				resolved, e = h.credentialOpsProxy(c.Request.Context(), source.AccountID, *cfg)
				if e == nil {
					task, e = h.db.RetryCredentialOpsTask(c.Request.Context(), id, database.CredentialOpsRetryRoute{ProxyURL: resolved, ConfigRevision: cfg.Revision})
				}
			}
		} else if e == nil {
			task, e = h.db.RetryCredentialOpsTask(c.Request.Context(), id)
		}

	}

	if e != nil {
		c.JSON(409, gin.H{"error": "task cannot be retried"})
		return
	}
	if task == nil {
		c.Status(404)
		return
	}
	if task.Status == "succeeded" && task.OAuthProfile != "excel" {
		unlock := h.store.LockCredentialOpsRecovery()
		defer unlock()
		if err := h.reconcileCredentialOpsLogin(c.Request.Context(), task.ID, task.AccountID); err != nil {
			c.JSON(503, gin.H{"error": "credentials committed; runtime recovery pending", "account_id": task.AccountID})
			return
		}
	}
	c.JSON(202, task)
}
func (h *Handler) RemoveCredentialOpsMonitor(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if e := h.db.DeleteCredentialOpsMonitor(c.Request.Context(), id); e != nil {
		c.Status(500)
		return
	}
	c.Status(204)
}
func (h *Handler) credentialOpsProxy(ctx context.Context, id int64, cfg database.CredentialOpsLoginConfigRow) (string, error) {
	if id > 0 && h.store != nil {
		a := h.store.FindByID(id)
		if a == nil {
			_ = h.store.LoadAccountByID(ctx, id)
			a = h.store.FindByID(id)
		}
		if a == nil && cfg.ProxySource == "account" {
			return "", errors.New("account egress cannot be resolved")
		}
		if a != nil {
			resolved, usable := h.store.ResolveUsableProxyForAccount(a)
			if !usable {
				return "", errors.New("account egress unavailable")
			}
			if cfg.ProxySource == "account" {
				return resolved, nil
			}
		}
	}
	switch cfg.ProxySource {
	case "mihomo":
		return "", nil
	case "direct":
		return "", nil
	case "proxy":
		rows, e := h.db.ListProxies(ctx)
		if e != nil {
			return "", e
		}
		for _, p := range rows {
			if p.ID == cfg.ProxyID && p.Enabled {
				return p.URL, nil
			}
		}
		return "", errors.New("selected proxy unavailable")
	default:
		if h.store != nil {
			return h.store.GetProxyURL(), nil
		}
		if cfg.ProxyURLCiphertext != "" {
			_, dec := credentialops.EnvCrypto()
			return dec(cfg.ProxyURLCiphertext)
		}
		return "", errors.New("effective login egress cannot be resolved")
	}
}

type credentialWorkerSnapshot struct {
	Mode          string    `json:"mode"`
	Reason        string    `json:"reason,omitempty"`
	State         string    `json:"state"`
	Error         string    `json:"error,omitempty"`
	ID            string    `json:"id"`
	Version       string    `json:"version"`
	Capabilities  []string  `json:"capabilities"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	Online        bool      `json:"online"`
}

var credentialWorkers sync.Map
var credentialMihomoLeases sync.Map

func (h *Handler) recordCredentialWorker(id, version string, capabilities []string) {
	credentialWorkers.Store(h, credentialWorkerSnapshot{ID: id, Version: version, Capabilities: capabilities, LastHeartbeat: time.Now().UTC(), Online: true})
}
func (h *Handler) credentialWorkerStatus() credentialWorkerSnapshot {
	if v, ok := credentialWorkers.Load(h); ok {
		r := v.(credentialWorkerSnapshot)
		r.Online = time.Since(r.LastHeartbeat) < 90*time.Second
		r.Mode = "standalone"
		if os.Getenv("CODEX2API_CREDENTIAL_OPS_RUNTIME_DIR") != "" {
			r.Mode = "managed"
		}
		r.State = "online"
		if !r.Online {
			r.State = "offline"
			r.Error = "worker heartbeat expired"
			r.Reason = r.Error
		}
		return r
	}
	return credentialWorkerSnapshot{Mode: "local_worker", State: "offline", Reason: "local worker has not connected", Error: "local worker has not connected", Capabilities: []string{}}
}

func (h *Handler) CredentialOpsWorkerRuntime(c *gin.Context) {
	var in struct {
		WorkerID     string   `json:"worker_id"`
		Version      string   `json:"version"`
		Capabilities []string `json:"capabilities"`
	}
	if c.ShouldBindJSON(&in) != nil || in.WorkerID == "" {
		c.Status(400)
		return
	}
	h.recordCredentialWorker(in.WorkerID, in.Version, in.Capabilities)
	r, e := h.db.CredentialOpsRuntime(c.Request.Context())
	if e != nil {
		c.Status(500)
		return
	}
	c.JSON(200, r)
}

func (h *Handler) ReleaseCredentialOpsWorker(c *gin.Context) {
	var in credentialWorkerRequest
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	if c.ShouldBindJSON(&in) != nil || in.WorkerID == "" || in.Attempt < 1 {
		c.Status(400)
		return
	}
	if e := h.db.ReleaseCredentialOpsWorkerRoute(c.Request.Context(), id, in.WorkerID, in.Attempt); e != nil {
		c.Status(409)
		return
	}
	if release, ok := credentialMihomoLeases.LoadAndDelete(fmt.Sprintf("%d:%d", id, in.Attempt)); ok {
		release.(func())()
	}
	c.Status(204)
}
