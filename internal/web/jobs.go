package web

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const maxLogLines = 500

// jobs runs one background task at a time and keeps its log for the UI.
type jobs struct {
	base context.Context

	mu      sync.Mutex
	running bool
	name    string
	log     []string
	err     string
	cancel  context.CancelFunc
}

type jobView struct {
	Running bool     `json:"running"`
	Name    string   `json:"name"`
	Log     []string `json:"log"`
	Error   string   `json:"error,omitempty"`
}

var errBusy = errors.New("another job is already running")

// start launches fn in the background; it fails if a job is already running.
func (j *jobs) start(name string, fn func(ctx context.Context, logf func(string, ...any)) error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return errBusy
	}
	ctx, cancel := context.WithCancel(j.base)
	j.running, j.name, j.log, j.err, j.cancel = true, name, nil, "", cancel
	go func() {
		err := fn(ctx, j.logf)
		j.mu.Lock()
		defer j.mu.Unlock()
		j.running = false
		switch {
		case errors.Is(err, context.Canceled):
			j.err = "cancelled"
		case err != nil:
			j.err = err.Error()
		}
		cancel()
	}()
	return nil
}

func (j *jobs) logf(format string, args ...any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.log = append(j.log, fmt.Sprintf(format, args...))
	if len(j.log) > maxLogLines {
		j.log = j.log[len(j.log)-maxLogLines:]
	}
}

func (j *jobs) stop() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		j.cancel()
	}
}

func (j *jobs) view() jobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return jobView{Running: j.running, Name: j.name, Log: append([]string(nil), j.log...), Error: j.err}
}
