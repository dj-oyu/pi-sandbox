package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusTimedOut  Status = "timed_out"
)

type Job struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Command    string `json:"command"`
	Status     Status `json:"status"`
	ExitCode   int    `json:"exitCode"`
	TimedOut   bool   `json:"timedOut"`
	DurationMs int64  `json:"durationMs"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	CreatedAt  string `json:"createdAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

type Runner struct {
	mu      sync.RWMutex
	jobs    map[string]*Job
	order   []string
	maxKeep int
	sem     chan struct{}
	seq     atomic.Uint64
}

func NewRunner(maxWorkers int) *Runner {
	if maxWorkers < 1 {
		maxWorkers = 4
	}
	return &Runner{
		jobs:    make(map[string]*Job),
		maxKeep: 200,
		sem:     make(chan struct{}, maxWorkers),
	}
}

func (r *Runner) RunSync(ctx context.Context, kind, label, exe string, args []string, cwd string, timeout time.Duration) *Job {
	job := r.enqueue(kind, label, exe, args, cwd, timeout)
	// Wait until finished
	for {
		j, _ := r.Get(job.ID)
		if j != nil && j.Status != StatusQueued && j.Status != StatusRunning {
			return j
		}
		select {
		case <-ctx.Done():
			return j
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (r *Runner) RunAsync(kind, label, exe string, args []string, cwd string, timeout time.Duration) *Job {
	return r.enqueue(kind, label, exe, args, cwd, timeout)
}

func (r *Runner) enqueue(kind, label, exe string, args []string, cwd string, timeout time.Duration) *Job {
	id := fmt.Sprintf("job-%d-%d", time.Now().UnixMilli(), r.seq.Add(1))
	job := &Job{
		ID:        id,
		Kind:      kind,
		Command:   label,
		Status:    StatusQueued,
		ExitCode:  -1,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}

	r.mu.Lock()
	r.jobs[id] = job
	r.order = append(r.order, id)
	if len(r.order) > r.maxKeep {
		evict := r.order[0]
		r.order = r.order[1:]
		delete(r.jobs, evict)
	}
	r.mu.Unlock()

	go r.execute(job, exe, args, cwd, timeout)
	return job
}

func (r *Runner) execute(job *Job, exe string, args []string, cwd string, timeout time.Duration) {
	r.sem <- struct{}{}
	defer func() { <-r.sem }()

	started := time.Now()
	r.mu.Lock()
	job.Status = StatusRunning
	r.mu.Unlock()

	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	elapsed := time.Since(started).Milliseconds()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	exitCode := 0
	status := StatusCompleted
	if timedOut {
		exitCode = 124
		status = StatusTimedOut
	} else if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
		status = StatusFailed
	}

	r.mu.Lock()
	job.Status = status
	job.ExitCode = exitCode
	job.TimedOut = timedOut
	job.DurationMs = elapsed
	job.Stdout = tailString(stdout.String(), 32*1024)
	job.Stderr = tailString(stderr.String(), 16*1024)
	job.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	r.mu.Unlock()
}

func (r *Runner) Get(id string) (*Job, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.jobs[id]
	if !ok {
		return nil, false
	}
	cp := *j
	return &cp, true
}

func (r *Runner) List() []Job {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Job, 0, len(r.order))
	for i := len(r.order) - 1; i >= 0; i-- {
		if j, ok := r.jobs[r.order[i]]; ok {
			out = append(out, *j)
		}
	}
	return out
}

func tailString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}
