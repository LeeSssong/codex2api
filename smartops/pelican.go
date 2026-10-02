package smartops

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

type PelicanJob struct {
	ID              int64   `json:"id"`
	AccountID       int64   `json:"account_id"`
	GroupIDs        []int64 `json:"group_ids"`
	Model           string  `json:"model"`
	Prompt          string  `json:"prompt"`
	ReasoningEffort string  `json:"reasoning_effort"`
	Samples         int     `json:"samples"`
	Parallel        int     `json:"parallel"`
	Retries         int     `json:"retries"`
	MaxHistory      int     `json:"max_history"`
}
type PelicanResult struct {
	JobID          int64            `json:"job_id"`
	AccountID      int64            `json:"account_id"`
	Sample         int              `json:"sample"`
	Status         string           `json:"status"`
	Output         string           `json:"output"`
	Error          string           `json:"error"`
	Latency        time.Duration    `json:"latency"`
	FirstContentMS *int64           `json:"first_content_ms,omitempty"`
	InputTokens    *int64           `json:"input_tokens,omitempty"`
	OutputTokens   *int64           `json:"output_tokens,omitempty"`
	StartedAt      time.Time        `json:"started_at"`
	FinishedAt     time.Time        `json:"finished_at"`
	CostUSD        *float64         `json:"cost_usd,omitempty"`
	CostIncomplete bool             `json:"cost_incomplete"`
	Attempts       []PelicanAttempt `json:"attempts"`
	LeaseOwner     string           `json:"-"`
}
type PelicanAttempt struct {
	AccountID int64    `json:"account_id"`
	Error     string   `json:"error"`
	CostUSD   *float64 `json:"cost_usd,omitempty"`
}

var pelicanHTML = regexp.MustCompile(`(?i)<(?:!doctype\s+html|html|svg)[\s>]`)

func ValidatePelicanOutput(output string) error {
	if len(output) > 1<<20 {
		return errors.New("output exceeds 1 MiB")
	}
	if !pelicanHTML.MatchString(output) {
		return errors.New("model returned no HTML or SVG document")
	}
	return nil
}
func ValidatePelicanJob(j PelicanJob) error {
	if j.AccountID < 0 || j.AccountID == 0 && len(j.GroupIDs) == 0 {
		return errors.New("account or group is required")
	}
	if strings.TrimSpace(j.Model) == "" || len(j.Model) > 200 || len(j.Prompt) > 16000 || j.Samples < 1 || j.Samples > 100 || j.Parallel < 1 || j.Parallel > 10 || j.Parallel > j.Samples || j.Retries < 0 || j.Retries > 5 {
		return errors.New("invalid test parameters")
	}
	if j.MaxHistory != 0 && (j.MaxHistory < 1 || j.MaxHistory > 1000) {
		return errors.New("history limit must be 1-1000")
	}
	seen := map[int64]bool{}
	for _, id := range j.GroupIDs {
		if id <= 0 || seen[id] {
			return errors.New("invalid group")
		}
		seen[id] = true
	}
	return nil
}
