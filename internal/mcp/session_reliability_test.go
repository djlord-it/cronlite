package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/mark3labs/mcp-go/server"
)

func TestMCPIdleCleanupPreservesActiveStreamAndShutdown(t *testing.T) {
	var registered, unregistered atomic.Int64
	hooks := &server.Hooks{}
	hooks.AddOnRegisterSession(func(context.Context, server.ClientSession) { registered.Add(1) })
	hooks.AddOnUnregisterSession(func(context.Context, server.ClientSession) { unregistered.Add(1) })
	sdk := server.NewMCPServer("reliability", "1", server.WithHooks(hooks), server.WithToolCapabilities(false))
	repo := &mockAPIKeyRepo{getKeyByHashFn: func(context.Context, string) (domain.APIKey, error) {
		return domain.APIKey{Enabled: true, Namespace: "test"}, nil
	}}
	handler := mountHTTP(sdk, repo, "", 100_000, 300*time.Millisecond, 50*time.Millisecond)
	t.Cleanup(func() {
		if err := handler.Shutdown(context.Background()); err != nil {
			t.Errorf("MCP shutdown: %v", err)
		}
	})
	initialize := func() string {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
		r.Header.Set("Authorization", "Bearer test")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get(server.HeaderKeySessionID) == "" {
			t.Fatalf("initialize: status=%d body=%s", w.Code, w.Body.String())
		}
		return w.Header().Get(server.HeaderKeySessionID)
	}
	profileDir := os.Getenv("CRONLITE_RELIABILITY_PROFILE_DIR")
	profile := func(name string) {
		t.Helper()
		if profileDir == "" {
			return
		}
		f, err := os.Create(filepath.Join(profileDir, name+".heap"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := pprof.WriteHeapProfile(f); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var before, retained, swept runtime.MemStats
	runtime.ReadMemStats(&before)
	profile("baseline")
	count := 20
	if profileDir != "" {
		count = 1000
	}
	for range count {
		initialize()
	}
	runtime.GC()
	runtime.ReadMemStats(&retained)
	profile("retained")
	activeID := initialize()
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	r, err := http.NewRequest(http.MethodGet, httpServer.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set(server.HeaderKeySessionID, activeID)
	response, err := httpServer.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("GET status=%d", response.StatusCode)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for registered.Load()-unregistered.Load() > 1 {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("abandoned sessions were not reclaimed")
		}
	}
	if registered.Load()-unregistered.Load() != 1 {
		t.Fatal("active stream was swept")
	}
	runtime.GC()
	runtime.ReadMemStats(&swept)
	profile("swept")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := handler.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := httpServer.Config.Shutdown(ctx); err != nil {
		t.Fatalf("active stream held HTTP shutdown open: %v", err)
	}
	if registered.Load() != unregistered.Load() {
		t.Fatal("sessions retained after shutdown")
	}
	_, _ = io.Copy(io.Discard, response.Body)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("admitted request after shutdown: %d", w.Code)
	}
	t.Logf("initialized=%d abandoned_reclaimed=%d active_preserved=1 shutdown_live=0 baseline_heap=%d retained_heap=%d swept_heap=%d reclaimed=%d", count+1, count, before.HeapAlloc, retained.HeapAlloc, swept.HeapAlloc, int64(retained.HeapAlloc)-int64(swept.HeapAlloc))
}
