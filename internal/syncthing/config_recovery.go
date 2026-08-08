// Package syncthing owns integration with the pinned upstream Syncthing
// process and its durable configuration.
package syncthing

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const MaxConfigBytes int64 = 8 * 1024 * 1024

var ErrNoKnownGoodConfig = errors.New("syncthing config recovery: identity exists without a known-good config")

type RecoveryState string

const (
	RecoveryReady RecoveryState = "ready"
	RecoveryClean RecoveryState = "factory-clean"
)

type RecoveryResult struct {
	State   RecoveryState
	Changed bool
}

type SyncFilesystemFunc func(string) error

type inspectedFile struct {
	path   string
	exists bool
	valid  bool
}

// RecoverConfig implements SYNC-1's config.xml/config.xml.tmp/config.xml.bak
// state table. It never promotes the temporary file: only the steady config or
// the last known-good backup may survive recovery.
func RecoverConfig(configDir string, syncFilesystem SyncFilesystemFunc) (RecoveryResult, error) {
	if syncFilesystem == nil {
		syncFilesystem = syncFilesystemAt
	}
	if err := requireRealDirectory(configDir); err != nil {
		return RecoveryResult{}, err
	}

	config, err := inspectXML(filepath.Join(configDir, "config.xml"))
	if err != nil {
		return RecoveryResult{}, err
	}
	temporary, err := inspectXML(filepath.Join(configDir, "config.xml.tmp"))
	if err != nil {
		return RecoveryResult{}, err
	}
	backup, err := inspectXML(filepath.Join(configDir, "config.xml.bak"))
	if err != nil {
		return RecoveryResult{}, err
	}

	if config.valid {
		changed := false
		if temporary.exists {
			if err := os.Remove(temporary.path); err != nil {
				return RecoveryResult{}, fmt.Errorf("discard interrupted config temporary: %w", err)
			}
			changed = true
		}
		if backup.exists && !backup.valid {
			if err := os.Remove(backup.path); err != nil {
				return RecoveryResult{}, fmt.Errorf("discard invalid config backup: %w", err)
			}
			changed = true
		}
		if changed {
			if err := syncFilesystem(configDir); err != nil {
				return RecoveryResult{}, fmt.Errorf("flush recovered config filesystem: %w", err)
			}
		}
		return RecoveryResult{State: RecoveryReady, Changed: changed}, nil
	}

	if backup.valid {
		if config.exists {
			if err := os.Remove(config.path); err != nil {
				return RecoveryResult{}, fmt.Errorf("remove unusable config: %w", err)
			}
		}
		if temporary.exists {
			if err := os.Remove(temporary.path); err != nil {
				return RecoveryResult{}, fmt.Errorf("discard interrupted config temporary: %w", err)
			}
		}
		if err := os.Rename(backup.path, config.path); err != nil {
			return RecoveryResult{}, fmt.Errorf("restore known-good config backup: %w", err)
		}
		if err := syncFilesystem(configDir); err != nil {
			return RecoveryResult{}, fmt.Errorf("flush restored config filesystem: %w", err)
		}
		restored, err := inspectXML(config.path)
		if err != nil {
			return RecoveryResult{}, err
		}
		if !restored.valid {
			return RecoveryResult{}, errors.New("restored config backup no longer parses")
		}
		return RecoveryResult{State: RecoveryReady, Changed: true}, nil
	}

	identityExists, err := hasAnyIdentity(configDir)
	if err != nil {
		return RecoveryResult{}, err
	}
	if identityExists {
		return RecoveryResult{}, ErrNoKnownGoodConfig
	}

	changed := false
	for _, file := range []inspectedFile{config, temporary, backup} {
		if !file.exists {
			continue
		}
		if err := os.Remove(file.path); err != nil {
			return RecoveryResult{}, fmt.Errorf("discard unusable factory-clean config file %s: %w", filepath.Base(file.path), err)
		}
		changed = true
	}
	if changed {
		if err := syncFilesystem(configDir); err != nil {
			return RecoveryResult{}, fmt.Errorf("flush factory-clean config filesystem: %w", err)
		}
	}
	return RecoveryResult{State: RecoveryClean, Changed: changed}, nil
}

func ValidateXML(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxConfigBytes {
		return fmt.Errorf("config is not a regular file at or below %d bytes", MaxConfigBytes)
	}

	decoder := xml.NewDecoder(io.LimitReader(file, MaxConfigBytes+1))
	rootCount := 0
	depth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				rootCount++
				if typed.Name.Local != "configuration" {
					return fmt.Errorf("unexpected config root %q", typed.Name.Local)
				}
			}
			depth++
		case xml.EndElement:
			depth--
			if depth < 0 {
				return errors.New("unexpected closing XML element")
			}
		}
	}
	if rootCount != 1 || depth != 0 {
		return errors.New("config must contain exactly one complete XML root")
	}
	return nil
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("validate config directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("validate config directory: not a real directory")
	}
	return nil
}

func inspectXML(path string) (inspectedFile, error) {
	result := inspectedFile{path: path}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("inspect %s: %w", filepath.Base(path), err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return result, fmt.Errorf("inspect %s: not a real regular file", filepath.Base(path))
	}
	if info.Size() > MaxConfigBytes {
		return result, fmt.Errorf("inspect %s: exceeds %d-byte limit", filepath.Base(path), MaxConfigBytes)
	}
	result.exists = true
	result.valid = ValidateXML(path) == nil
	return result, nil
}

func hasAnyIdentity(configDir string) (bool, error) {
	found := false
	for _, name := range []string{"cert.pem", "key.pem"} {
		path := filepath.Join(configDir, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect identity %s: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, fmt.Errorf("inspect identity %s: not a real regular file", name)
		}
		found = true
	}
	return found, nil
}
