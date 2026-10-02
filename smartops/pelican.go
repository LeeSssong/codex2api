package smartops

import (
	"context"
	"errors"
	"sync"
	"time"
)

type PelicanJob struct {
	ID         int64
	AccountID  int64
	Model      string
	Samples    int
	Parallel   int
	Retries    int
	MaxHistory int
}
type PelicanResult struct {
	JobID, AccountID      int64
	Sample                int
	Status                string
	Output                string
	Error                 string
	Latency               time.Duration
	StartedAt, FinishedAt time.Time
}
type PelicanProbe func(context.Context, int64, string) (string, error)
type PelicanHistory interface {
	SavePelicanResult(context.Context, PelicanResult) error
}

// RunGroup executes a leased job with bounded parallelism. A failed sample is
// retried in-place; cancellation stops new attempts while in-flight probes can
// honor the shared context and terminate promptly.
func RunGroup(ctx context.Context, job PelicanJob, probe PelicanProbe, history PelicanHistory) ([]PelicanResult, error) {
	if probe == nil || history == nil {
		return nil, errors.New("probe and history are required")
	}
	if job.Samples < 1 {
		job.Samples = 1
	}
	if job.Parallel < 1 {
		job.Parallel = 1
	}
	if job.Parallel > job.Samples {
		job.Parallel = job.Samples
	}
	if job.Retries < 0 {
		job.Retries = 0
	}
	results := make([]PelicanResult, job.Samples)
	sem := make(chan struct{}, job.Parallel)
	var wg sync.WaitGroup
	for i := 0; i < job.Samples; i++ {
		select {
		case <-ctx.Done():
			return results[:i], ctx.Err()
		default:
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var last error
			started := time.Now()
			for attempt := 0; attempt <= job.Retries; attempt++ {
				if err := ctx.Err(); err != nil {
					last = err
					break
				}
				output, err := probe(ctx, job.AccountID, job.Model)
				if err == nil && output == "" {
					err = errors.New("empty probe output")
				}
				if err == nil && output != "" {
					finished := time.Now()
					results[index] = PelicanResult{JobID: job.ID, AccountID: job.AccountID, Sample: index, Status: "success", Output: output, Latency: finished.Sub(started), StartedAt: started, FinishedAt: finished}
					_ = history.SavePelicanResult(ctx, results[index])
					return
				}
				last = err
			}
			finished := time.Now()
			results[index] = PelicanResult{JobID: job.ID, AccountID: job.AccountID, Sample: index, Status: "failed", Error: last.Error(), Latency: finished.Sub(started), StartedAt: started, FinishedAt: finished}
			_ = history.SavePelicanResult(ctx, results[index])
		}(i)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return results, err
	}
	return results, nil
}
