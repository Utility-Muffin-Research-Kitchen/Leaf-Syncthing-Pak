package controller

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDebugLoggingHasFixedExpiry(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), loggingStateName)
	manager, err := newLoggingManager(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if manager.Status().Level != "normal" {
		t.Fatal("logging did not default to normal")
	}
	if err := manager.Set("debug"); err != nil {
		t.Fatal(err)
	}
	if got := manager.Status(); got.Level != "debug" || got.DebugExpires != now.Add(debugLifetime).Format(time.RFC3339) {
		t.Fatalf("debug status = %+v", got)
	}
	now = now.Add(debugLifetime + time.Second)
	if got := manager.Status(); got.Level != "normal" || got.DebugExpires != "" {
		t.Fatalf("expired status = %+v", got)
	}
	reloaded, err := newLoggingManager(path, func() time.Time { return now })
	if err != nil || reloaded.Status().Level != "normal" {
		t.Fatalf("reloaded logging = %+v, %v", reloaded, err)
	}
}
