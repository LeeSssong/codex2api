// Adapted from Sub2API account_token_guard.go (LGPL-3.0).
package tokenguard

import "time"

type ReloginAccount struct {
	AccountID int64  `json:"account_id"`
	Email     string `json:"email"`
	Password  string `json:"password"`
	MFASecret string `json:"mfa_secret"`
}

// Config 是页面上的全部可配置项。
type Config struct {
	Enabled             bool              `json:"enabled"`
	GroupIDs            []int64           `json:"group_ids"`
	IntervalSeconds     int               `json:"interval_seconds"`
	ProbeEndpoint       string            `json:"probe_endpoint"`
	ProbeModel          string            `json:"probe_model"`
	ProbeHeaders        map[string]string `json:"probe_headers"`
	ProbeTimeoutSeconds int               `json:"probe_timeout_seconds"`
	ProbeConcurrency    int               `json:"probe_concurrency"`
	MaxProbePerCycle    int               `json:"max_probe_per_cycle"`
	AutoRelogin         bool              `json:"auto_relogin"`
	ReloginEndpoint     string            `json:"relogin_endpoint"`
	ReloginHeaders      map[string]string `json:"relogin_headers"`
	ReloginAccounts     []ReloginAccount  `json:"relogin_accounts"`
	RestoreSchedulable  bool              `json:"restore_schedulable"`
	FailStreakThreshold int               `json:"fail_streak_threshold"`
	BarkKey             string            `json:"bark_key"`
	NotifyOnFix         bool              `json:"notify_on_fix"`
	NotifyOnFail        bool              `json:"notify_on_fail"`
}

// AccountState 是一个账号最近一次巡检的展示状态。
type AccountState struct {
	AccountID     int64      `json:"account_id"`
	AccountName   string     `json:"account_name"`
	AccountStatus string     `json:"account_status"`
	Schedulable   bool       `json:"schedulable"`
	ProbeState    string     `json:"probe_state"`
	ProbeDetail   string     `json:"probe_detail"`
	LatencyMS     int        `json:"latency_ms"`
	FailStreak    int        `json:"fail_streak"`
	LastProbeAt   *time.Time `json:"last_probe_at"`
	LastFixAt     *time.Time `json:"last_fix_at"`
	LastFixAction string     `json:"last_fix_action"`
	LastFixResult string     `json:"last_fix_result"`
	NeedsRelogin  bool       `json:"needs_relogin"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Event 是一条巡检 / 修复日志。
type Event struct {
	ID          int64     `json:"id"`
	AccountID   int64     `json:"account_id"`
	AccountName string    `json:"account_name"`
	Kind        string    `json:"kind"`
	Detail      string    `json:"detail"`
	LatencyMS   int       `json:"latency_ms"`
	CreatedAt   time.Time `json:"created_at"`
}

// Stats 描述最近一轮巡检的结果。
type Stats struct {
	Probed     int   `json:"probed"`
	Healthy    int   `json:"healthy"`
	AuthFailed int   `json:"auth_failed"`
	Transient  int   `json:"transient"`
	Repaired   int   `json:"repaired"`
	StateFixed int   `json:"state_fixed"`
	Failed     int   `json:"failed"`
	DurationMS int64 `json:"duration_ms"`
	StartedAt  int64 `json:"started_at"`
}

// Runtime 是页头展示的运行信息。
type Runtime struct {
	JobID        string     `json:"job_id"`
	JobState     string     `json:"job_state"`
	Cancellation bool       `json:"cancellation"`
	Running      bool       `json:"running"`
	LastRun      *time.Time `json:"last_run"`
	LastMessage  string     `json:"last_message"`
	Stats        Stats      `json:"stats"`
}

// Status 是状态接口返回体。
type Status struct {
	ModuleEnabled bool           `json:"module_enabled"`
	Config        Config         `json:"config"`
	Accounts      []AccountState `json:"accounts"`
	Events        []Event        `json:"events"`
	Runtime       Runtime        `json:"runtime"`
}
