package main

import (
	"context"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/scheduler"
)

func TestLeaderRuntime_IgnoresCancelledStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lr := &leaderRuntime{sched: scheduler.New(scheduler.Config{TickInterval: time.Hour}, nil, nil, nil)}
	defer lr.stop()
	lr.start(ctx)
	lr.mu.Lock()
	running := lr.running
	lr.mu.Unlock()
	if running {
		t.Fatal("cancelled startup left leader runtime marked running")
	}
}
