package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

const (
	loggingStateName = "logging.json"
	debugLifetime    = 15 * time.Minute
)

type loggingDocument struct {
	Schema       int       `json:"schema"`
	Level        string    `json:"level"`
	DebugExpires time.Time `json:"debug_expires,omitempty"`
}

type loggingManager struct {
	mu    sync.Mutex
	path  string
	now   func() time.Time
	state loggingDocument
}

func newLoggingManager(path string, now func() time.Time) (*loggingManager, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("logging state requires an absolute path")
	}
	if now == nil {
		now = time.Now
	}
	manager := &loggingManager{path: path, now: now, state: loggingDocument{Schema: 1, Level: "normal"}}
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 4096 {
			return nil, errors.New("logging state is unsafe")
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&manager.state); err != nil {
			return nil, err
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || manager.state.Schema != 1 ||
			(manager.state.Level != "normal" && manager.state.Level != "debug") {
			return nil, errors.New("logging state is unsupported")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if manager.expireLocked() || os.IsNotExist(err) {
		if err := manager.persistLocked(); err != nil {
			return nil, err
		}
	}
	return manager, nil
}

func (manager *loggingManager) Set(level string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if level != "normal" && level != "debug" {
		return errors.New("unsupported log level")
	}
	manager.state.Level = level
	manager.state.DebugExpires = time.Time{}
	if level == "debug" {
		manager.state.DebugExpires = manager.now().UTC().Add(debugLifetime)
	}
	return manager.persistLocked()
}

func (manager *loggingManager) Status() uicontrol.LoggingStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.expireLocked() {
		_ = manager.persistLocked()
	}
	status := uicontrol.LoggingStatus{Level: manager.state.Level}
	if !manager.state.DebugExpires.IsZero() {
		status.DebugExpires = manager.state.DebugExpires.UTC().Format(time.RFC3339)
	}
	return status
}

func (manager *loggingManager) Debug() bool {
	return manager.Status().Level == "debug"
}

func (manager *loggingManager) expireLocked() bool {
	if manager.state.Level != "debug" {
		manager.state.DebugExpires = time.Time{}
		return false
	}
	now := manager.now().UTC()
	if manager.state.DebugExpires.IsZero() || !now.Before(manager.state.DebugExpires) || manager.state.DebugExpires.Sub(now) > debugLifetime {
		manager.state.Level = "normal"
		manager.state.DebugExpires = time.Time{}
		return true
	}
	return false
}

func (manager *loggingManager) persistLocked() error {
	payload, err := json.Marshal(manager.state)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(filepath.Dir(manager.path), 0o700); err != nil {
		return err
	}
	temporary := manager.path + ".tmp"
	if err := removeSafeRegularIfExists(temporary); err != nil {
		return err
	}
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, manager.path); err != nil {
		return err
	}
	return syncStateFilesystem(filepath.Dir(manager.path))
}
