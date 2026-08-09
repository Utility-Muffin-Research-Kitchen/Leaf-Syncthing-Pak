package controller

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

const (
	firstSyncMarkerName       = ".leaf-first-sync.json"
	firstSyncMarkerTemporary  = ".leaf-first-sync.json.tmp"
	snapshotHeaderName        = "snapshot.json"
	snapshotHeaderTemporary   = "snapshot.json.tmp"
	snapshotManifestName      = "manifest.jsonl"
	snapshotFilesName         = "files"
	snapshotPartialPrefix     = ".partial-"
	firstSyncDocumentSchema   = 1
	maxFirstSyncDocumentBytes = 32 * 1024
	maxSnapshotEntries        = 250000
	maxSnapshotRelativePath   = 1024
)

type snapshotHeader struct {
	Schema         int    `json:"schema"`
	State          string `json:"state"`
	Epoch          uint64 `json:"epoch"`
	FolderID       string `json:"folder_id"`
	CardID         string `json:"card_id"`
	SourceID       string `json:"source_id"`
	Kind           string `json:"kind"`
	SourceRelative string `json:"source_relative"`
	Name           string `json:"name"`
	CreatedAt      string `json:"created_at"`
	FileCount      int    `json:"file_count"`
	DirectoryCount int    `json:"directory_count"`
	ContentBytes   int64  `json:"content_bytes"`
}

type snapshotManifestEntry struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	Size     int64  `json:"size,omitempty"`
	Modified string `json:"modified,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
}

type firstSyncMarker struct {
	Schema                    int    `json:"schema"`
	State                     string `json:"state"`
	Epoch                     uint64 `json:"epoch"`
	FolderID                  string `json:"folder_id"`
	CardID                    string `json:"card_id"`
	Kind                      string `json:"kind"`
	FolderType                string `json:"folder_type"`
	Mode                      string `json:"mode"`
	SnapshotName              string `json:"snapshot_name,omitempty"`
	HubVersioningAcknowledged bool   `json:"hub_versioning_acknowledged"`
	ExplicitStart             bool   `json:"explicit_start"`
	CompletedAt               string `json:"completed_at"`
}

type firstSyncProgress struct {
	State          string
	SnapshotName   string
	FileCount      int
	DirectoryCount int
	ContentBytes   int64
	Message        string
}

type firstSyncOptions struct {
	Now              func() time.Time
	Random           io.Reader
	SyncFilesystem   func(string) error
	RequireFreeSpace func(string, int64) error
	Fault            func(string) error
}

type firstSyncManager struct {
	mu       sync.Mutex
	options  firstSyncOptions
	progress map[string]firstSyncProgress
}

func newFirstSyncManager(folders []syncthing.ConfiguredFolder, inventory []cards.Card, controls *folderControlStore, options firstSyncOptions) (*firstSyncManager, error) {
	manager := &firstSyncManager{options: defaultFirstSyncOptions(options), progress: make(map[string]firstSyncProgress)}
	if err := manager.recover(folders, inventory, controls); err != nil {
		return nil, err
	}
	return manager, nil
}

func defaultFirstSyncOptions(options firstSyncOptions) firstSyncOptions {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.SyncFilesystem == nil {
		options.SyncFilesystem = syncStateFilesystem
	}
	if options.RequireFreeSpace == nil {
		options.RequireFreeSpace = leaf.RequireFreeSpace
	}
	return options
}

func (manager *firstSyncManager) recover(folders []syncthing.ConfiguredFolder, inventory []cards.Card, controls *folderControlStore) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	controlState := controls.Snapshot()
	for _, card := range inventory {
		if !usableEnrolledCard(card) {
			continue
		}
		for _, kind := range []string{"saves", "states"} {
			if err := recoverFirstSyncKind(card, kind); err != nil {
				return err
			}
		}
	}
	for _, folder := range folders {
		control := controlState[folder.ID]
		card, ok := cardForConfiguredFolder(folder, inventory, controlState)
		if !ok || !usableEnrolledCard(card) {
			// Card absence, read-only state, or duplicate identity is a transient
			// storage pause. Never rewrite the durable first-sync decision from an
			// unresolved mount path; verify the card marker when the card is next
			// present in a controller generation.
			if control.FirstSync {
				manager.progress[folder.ID] = firstSyncProgress{State: "required"}
			} else {
				manager.progress[folder.ID] = firstSyncProgress{State: "complete"}
			}
			continue
		}
		complete := false
		marker, markerOK, err := readFirstSyncMarker(card, folder, control.FirstSyncEpoch)
		if err != nil {
			return err
		}
		complete = markerOK && marker.State == "complete"
		if complete {
			// Complete is written before the final card-wide durability barrier.
			// If controller state still says first-sync, the previous process may
			// have died between that write and syncfs. Finish the barrier before
			// allowing recovery to clear the durable pause reason.
			if controlState[folder.ID].FirstSync {
				if err := manager.options.SyncFilesystem(card.Source.Root); err != nil {
					return fmt.Errorf("recover complete first-sync marker: %w", err)
				}
			}
			manager.progress[folder.ID] = firstSyncProgress{State: "complete", SnapshotName: marker.SnapshotName}
		} else {
			if !control.FirstSync {
				if err := controls.RequireFirstSync(folder.ID); err != nil {
					return err
				}
				control = controls.Snapshot()[folder.ID]
				manager.progress[folder.ID] = firstSyncProgress{State: "required"}
			} else if prepared, preparedOK, err := latestPreparedSnapshot(card, folder, control.FirstSyncEpoch); err != nil {
				return err
			} else if preparedOK {
				manager.progress[folder.ID] = progressFromSnapshot("ready", prepared)
			}
		}
		if err := controls.SetFirstSync(folder.ID, !complete); err != nil {
			return err
		}
		if _, ok := manager.progress[folder.ID]; !ok {
			manager.progress[folder.ID] = firstSyncProgress{State: "required"}
		}
	}
	return nil
}

func (manager *firstSyncManager) Progress(folderID string, firstSync bool) firstSyncProgress {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	progress, ok := manager.progress[folderID]
	if !ok {
		progress = firstSyncProgress{State: "required"}
	}
	if !firstSync {
		progress.State = "complete"
	}
	return progress
}

func (manager *firstSyncManager) Register(folderID string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.progress[folderID] = firstSyncProgress{State: "required"}
}

func (manager *firstSyncManager) Prepare(ctx context.Context, folder syncthing.ConfiguredFolder, card cards.Card, controls *folderControlStore) (firstSyncProgress, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if folder.Type == "sendonly" {
		return firstSyncProgress{}, errors.New("send-only seeding does not require a safety snapshot")
	}
	if !usableEnrolledCard(card) {
		return firstSyncProgress{}, errors.New("first sync requires the enrolled physical card to be present and writable")
	}
	control, ok := controls.Snapshot()[folder.ID]
	if !ok || !control.FirstSync || control.FirstSyncEpoch == 0 {
		return firstSyncProgress{}, errors.New("first-sync protection is not pending for this folder")
	}
	if err := validateFirstSyncBinding(folder, card, control); err != nil {
		return firstSyncProgress{}, err
	}
	manager.progress[folder.ID] = firstSyncProgress{State: "preparing"}
	progress, err := manager.prepareLocked(ctx, folder, card, control.FirstSyncEpoch)
	if err != nil {
		manager.progress[folder.ID] = firstSyncProgress{State: "error", Message: displayFirstSyncError(err)}
		return firstSyncProgress{}, err
	}
	manager.progress[folder.ID] = progress
	return progress, nil
}

func applyFirstSyncStatus(status uicontrol.Status, manager *firstSyncManager, controls *folderControlStore) uicontrol.Status {
	if manager == nil || controls == nil {
		return status
	}
	controlState := controls.Snapshot()
	for index := range status.Folders {
		progress := manager.Progress(status.Folders[index].ID, controlState[status.Folders[index].ID].FirstSync)
		status.Folders[index].FirstSyncState = progress.State
		status.Folders[index].SnapshotName = progress.SnapshotName
		status.Folders[index].SnapshotFiles = progress.FileCount
		status.Folders[index].SnapshotDirectories = progress.DirectoryCount
		status.Folders[index].SnapshotBytes = progress.ContentBytes
		status.Folders[index].FirstSyncMessage = progress.Message
	}
	return status
}

func (manager *firstSyncManager) prepareLocked(ctx context.Context, folder syncthing.ConfiguredFolder, card cards.Card, epoch uint64) (firstSyncProgress, error) {
	fileCount, directoryCount, contentBytes, err := scanSnapshotSource(folder.Path, folder.MarkerName)
	if err != nil {
		return firstSyncProgress{}, err
	}
	snapshotRoot := firstSyncKindRoot(card, folder.Kind)
	if err := ensureSafeDirectoryChain(card.Source.Root, snapshotRoot); err != nil {
		return firstSyncProgress{}, err
	}
	if err := manager.options.RequireFreeSpace(snapshotRoot, contentBytes); err != nil {
		return firstSyncProgress{}, err
	}
	random := make([]byte, 4)
	if _, err := io.ReadFull(manager.options.Random, random); err != nil {
		return firstSyncProgress{}, err
	}
	name := manager.options.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random)
	partial := filepath.Join(snapshotRoot, snapshotPartialPrefix+name)
	final := filepath.Join(snapshotRoot, name)
	if _, err := os.Lstat(partial); !os.IsNotExist(err) {
		if err == nil {
			return firstSyncProgress{}, errors.New("snapshot partial name already exists")
		}
		return firstSyncProgress{}, err
	}
	if _, err := os.Lstat(final); !os.IsNotExist(err) {
		if err == nil {
			return firstSyncProgress{}, errors.New("snapshot name already exists")
		}
		return firstSyncProgress{}, err
	}
	if err := os.Mkdir(partial, 0o700); err != nil {
		return firstSyncProgress{}, err
	}
	filesRoot := filepath.Join(partial, snapshotFilesName)
	if err := os.Mkdir(filesRoot, 0o700); err != nil {
		return firstSyncProgress{}, err
	}
	manifest, err := os.OpenFile(filepath.Join(partial, snapshotManifestName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return firstSyncProgress{}, err
	}
	buffered := bufio.NewWriterSize(manifest, 64*1024)
	encoder := json.NewEncoder(buffered)
	copiedFiles, copiedDirectories, copiedBytes, copyErr := copySnapshotTree(ctx, folder.Path, filesRoot, folder.MarkerName, encoder)
	if copyErr == nil {
		copyErr = buffered.Flush()
	}
	if copyErr == nil {
		copyErr = manifest.Sync()
	}
	if closeErr := manifest.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return firstSyncProgress{}, copyErr
	}
	if copiedFiles != fileCount || copiedDirectories != directoryCount || copiedBytes != contentBytes {
		return firstSyncProgress{}, errors.New("folder changed while the safety snapshot was being copied; retry after writes settle")
	}
	relative, err := leaf.RelativeWithin(card.Source.Root, folder.Path)
	if err != nil || relative == "." {
		return firstSyncProgress{}, errors.New("snapshot source is not a confined card content path")
	}
	header := snapshotHeader{
		Schema: firstSyncDocumentSchema, State: "pending", Epoch: epoch, FolderID: folder.ID,
		CardID: card.Identity.ID, SourceID: card.Source.ID, Kind: folder.Kind,
		SourceRelative: filepath.ToSlash(relative), Name: name,
		CreatedAt: manager.options.Now().UTC().Format(time.RFC3339), FileCount: copiedFiles,
		DirectoryCount: copiedDirectories, ContentBytes: copiedBytes,
	}
	if err := writeBoundedJSON(filepath.Join(partial, snapshotHeaderName), header); err != nil {
		return firstSyncProgress{}, err
	}
	if err := manager.fault("snapshot-copied"); err != nil {
		return firstSyncProgress{}, err
	}
	if err := manager.options.SyncFilesystem(card.Source.Root); err != nil {
		return firstSyncProgress{}, fmt.Errorf("flush safety snapshot: %w", err)
	}
	if err := manager.fault("snapshot-synced"); err != nil {
		return firstSyncProgress{}, err
	}
	header.State = "ready"
	if err := replaceBoundedJSON(filepath.Join(partial, snapshotHeaderName), snapshotHeaderTemporary, header); err != nil {
		return firstSyncProgress{}, err
	}
	if err := manager.options.SyncFilesystem(card.Source.Root); err != nil {
		return firstSyncProgress{}, fmt.Errorf("flush ready snapshot record: %w", err)
	}
	if err := manager.fault("snapshot-ready"); err != nil {
		return firstSyncProgress{}, err
	}
	if err := os.Rename(partial, final); err != nil {
		return firstSyncProgress{}, err
	}
	if err := manager.fault("snapshot-promoted"); err != nil {
		return firstSyncProgress{}, err
	}
	if err := manager.options.SyncFilesystem(card.Source.Root); err != nil {
		return firstSyncProgress{}, fmt.Errorf("flush promoted safety snapshot: %w", err)
	}
	if err := manager.fault("snapshot-committed"); err != nil {
		return firstSyncProgress{}, err
	}
	header, ok, err := readSnapshotHeader(final, folder, card, epoch)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("promoted snapshot did not validate")
		}
		return firstSyncProgress{}, err
	}
	return progressFromSnapshot("ready", header), nil
}

func (manager *firstSyncManager) Complete(folder syncthing.ConfiguredFolder, card cards.Card, controls *folderControlStore, hubAcknowledged bool) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !hubAcknowledged {
		return errors.New("hub versioning acknowledgment is required")
	}
	if !usableEnrolledCard(card) {
		return errors.New("first sync requires the enrolled physical card to be present and writable")
	}
	control, ok := controls.Snapshot()[folder.ID]
	if !ok || !control.FirstSync || control.FirstSyncEpoch == 0 {
		return errors.New("first-sync protection is not pending for this folder")
	}
	if err := validateFirstSyncBinding(folder, card, control); err != nil {
		return err
	}
	mode := "sendonly"
	snapshotName := ""
	if folder.Type != "sendonly" {
		header, ok, err := latestPreparedSnapshot(card, folder, control.FirstSyncEpoch)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("a durable safety snapshot is required before first sync")
		}
		mode = "snapshot"
		snapshotName = header.Name
		// A prior process can have died after promoting the snapshot directory
		// but before its final syncfs returned. Repeating the card-wide barrier
		// here is cheap compared with first sync and makes the snapshot durable
		// before the first completion-marker write in every retry path.
		if err := manager.options.SyncFilesystem(card.Source.Root); err != nil {
			return fmt.Errorf("confirm safety snapshot durability: %w", err)
		}
	}
	marker := firstSyncMarker{
		Schema: firstSyncDocumentSchema, State: "pending", Epoch: control.FirstSyncEpoch, FolderID: folder.ID,
		CardID: card.Identity.ID, Kind: folder.Kind, FolderType: folder.Type,
		Mode: mode, SnapshotName: snapshotName, HubVersioningAcknowledged: true,
		ExplicitStart: true, CompletedAt: manager.options.Now().UTC().Format(time.RFC3339),
	}
	root := firstSyncKindRoot(card, folder.Kind)
	if err := ensureSafeDirectoryChain(card.Source.Root, root); err != nil {
		return err
	}
	path := filepath.Join(root, firstSyncMarkerName)
	if err := replaceBoundedJSON(path, firstSyncMarkerTemporary, marker); err != nil {
		return err
	}
	if err := manager.fault("completion-pending-written"); err != nil {
		return err
	}
	if err := manager.options.SyncFilesystem(card.Source.Root); err != nil {
		return fmt.Errorf("flush pending first-sync marker: %w", err)
	}
	if err := manager.fault("completion-pending-synced"); err != nil {
		return err
	}
	marker.State = "complete"
	if err := replaceBoundedJSON(path, firstSyncMarkerTemporary, marker); err != nil {
		return err
	}
	if err := manager.fault("completion-ready-written"); err != nil {
		return err
	}
	if err := manager.options.SyncFilesystem(card.Source.Root); err != nil {
		return fmt.Errorf("flush complete first-sync marker: %w", err)
	}
	if err := manager.fault("completion-ready-synced"); err != nil {
		return err
	}
	if err := controls.SetFirstSync(folder.ID, false); err != nil {
		return err
	}
	if err := manager.fault("control-cleared"); err != nil {
		return err
	}
	manager.progress[folder.ID] = firstSyncProgress{State: "complete", SnapshotName: snapshotName}
	return nil
}

func (manager *firstSyncManager) Invalidate(folder syncthing.ConfiguredFolder, card cards.Card, controls *folderControlStore) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !usableEnrolledCard(card) {
		return errors.New("the enrolled physical card is unavailable")
	}
	path := filepath.Join(firstSyncKindRoot(card, folder.Kind), firstSyncMarkerName)
	if err := removeSafeRegularIfExists(path); err != nil {
		return err
	}
	if err := removeSafeRegularIfExists(filepath.Join(firstSyncKindRoot(card, folder.Kind), firstSyncMarkerTemporary)); err != nil {
		return err
	}
	if err := manager.options.SyncFilesystem(card.Source.Root); err != nil {
		return err
	}
	if err := controls.RequireFirstSync(folder.ID); err != nil {
		return err
	}
	manager.progress[folder.ID] = firstSyncProgress{State: "required"}
	return nil
}

func (manager *firstSyncManager) fault(point string) error {
	if manager.options.Fault == nil {
		return nil
	}
	return manager.options.Fault(point)
}

func scanSnapshotSource(root, markerName string) (int, int, int64, error) {
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return 0, 0, 0, errors.New("snapshot source is absent, symlinked, or not a directory")
	}
	files, directories := 0, 0
	var bytes int64
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || len(relative) > maxSnapshotRelativePath {
			return errors.New("snapshot contains an invalid or overlong relative path")
		}
		if relative == markerName {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return errors.New("managed marker is not a directory")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot source contains a symlink: %s", relative)
		}
		if entry.IsDir() {
			directories++
		} else if entry.Type().IsRegular() {
			fileInfo, err := entry.Info()
			if err != nil || fileInfo.Size() < 0 || bytes > int64(^uint64(0)>>1)-fileInfo.Size() {
				return errors.New("snapshot source size is invalid")
			}
			files++
			bytes += fileInfo.Size()
		} else {
			return fmt.Errorf("snapshot source contains a special file: %s", relative)
		}
		if files+directories > maxSnapshotEntries {
			return errors.New("snapshot source exceeds the entry limit")
		}
		return nil
	})
	return files, directories, bytes, err
}

func copySnapshotTree(ctx context.Context, sourceRoot, destinationRoot, markerName string, encoder *json.Encoder) (int, int, int64, error) {
	files, directories := 0, 0
	var bytes int64
	buffer := make([]byte, 128*1024)
	err := filepath.WalkDir(sourceRoot, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if sourcePath == sourceRoot {
			return nil
		}
		relative, err := filepath.Rel(sourceRoot, sourcePath)
		if err != nil || relative == "." || len(relative) > maxSnapshotRelativePath {
			return errors.New("snapshot contains an invalid or overlong relative path")
		}
		if relative == markerName {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return errors.New("managed marker is not a directory")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot source contains a symlink: %s", relative)
		}
		destinationPath := filepath.Join(destinationRoot, relative)
		if _, err := leaf.RelativeWithin(destinationRoot, destinationPath); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		manifest := snapshotManifestEntry{Path: filepath.ToSlash(relative), Modified: info.ModTime().UTC().Format(time.RFC3339Nano)}
		if entry.IsDir() {
			if err := os.Mkdir(destinationPath, 0o700); err != nil {
				return err
			}
			manifest.Type = "directory"
			directories++
		} else if entry.Type().IsRegular() {
			manifest.Type = "file"
			source, err := os.Open(sourcePath)
			if err != nil {
				return err
			}
			destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				_ = source.Close()
				return err
			}
			digest := sha256.New()
			written, copyErr := io.CopyBuffer(io.MultiWriter(destination, digest), source, buffer)
			if copyErr == nil && written != info.Size() {
				copyErr = errors.New("snapshot source changed size during copy")
			}
			if copyErr == nil {
				copyErr = destination.Sync()
			}
			if closeErr := destination.Close(); copyErr == nil {
				copyErr = closeErr
			}
			if closeErr := source.Close(); copyErr == nil {
				copyErr = closeErr
			}
			if copyErr != nil {
				return copyErr
			}
			_ = os.Chtimes(destinationPath, info.ModTime(), info.ModTime())
			manifest.Size = written
			manifest.SHA256 = hex.EncodeToString(digest.Sum(nil))
			files++
			bytes += written
		} else {
			return fmt.Errorf("snapshot source contains a special file: %s", relative)
		}
		if files+directories > maxSnapshotEntries {
			return errors.New("snapshot source exceeds the entry limit")
		}
		return encoder.Encode(manifest)
	})
	return files, directories, bytes, err
}

func recoverFirstSyncKind(card cards.Card, kind string) error {
	root := firstSyncKindRoot(card, kind)
	entries, exists, err := safeDirectoryEntries(root)
	if err != nil || !exists {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		switch {
		case strings.HasPrefix(entry.Name(), snapshotPartialPrefix):
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				return errors.New("first-sync partial snapshot is unsafe")
			}
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		case entry.Name() == firstSyncMarkerTemporary:
			if err := removeSafeRegularIfExists(path); err != nil {
				return err
			}
		}
	}
	markerPath := filepath.Join(root, firstSyncMarkerName)
	var marker firstSyncMarker
	present, err := readBoundedJSON(markerPath, &marker)
	if err != nil {
		return err
	}
	if present && marker.State != "complete" {
		if err := removeSafeRegularIfExists(markerPath); err != nil {
			return err
		}
	}
	return nil
}

func latestPreparedSnapshot(card cards.Card, folder syncthing.ConfiguredFolder, epoch uint64) (snapshotHeader, bool, error) {
	root := firstSyncKindRoot(card, folder.Kind)
	entries, exists, err := safeDirectoryEntries(root)
	if err != nil || !exists {
		return snapshotHeader{}, false, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return snapshotHeader{}, false, errors.New("snapshot history contains an unsafe entry")
		}
		names = append(names, entry.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names {
		header, ok, err := readSnapshotHeader(filepath.Join(root, name), folder, card, epoch)
		if err != nil {
			return snapshotHeader{}, false, err
		}
		if ok {
			return header, true, nil
		}
	}
	return snapshotHeader{}, false, nil
}

func readSnapshotHeader(root string, folder syncthing.ConfiguredFolder, card cards.Card, epoch uint64) (snapshotHeader, bool, error) {
	var header snapshotHeader
	present, err := readBoundedJSON(filepath.Join(root, snapshotHeaderName), &header)
	if err != nil || !present {
		return snapshotHeader{}, false, err
	}
	manifest, err := os.Lstat(filepath.Join(root, snapshotManifestName))
	if err != nil || manifest.Mode()&os.ModeSymlink != 0 || !manifest.Mode().IsRegular() || manifest.Size() < 0 {
		return snapshotHeader{}, false, errors.New("snapshot manifest is absent or unsafe")
	}
	if header.Schema != firstSyncDocumentSchema || header.State != "ready" || header.Epoch != epoch || epoch == 0 || header.FolderID != folder.ID ||
		header.CardID != card.Identity.ID || header.SourceID != card.Source.ID || header.Kind != folder.Kind ||
		header.Name != filepath.Base(root) || header.FileCount < 0 || header.DirectoryCount < 0 || header.ContentBytes < 0 {
		return snapshotHeader{}, false, nil
	}
	return header, true, nil
}

func readFirstSyncMarker(card cards.Card, folder syncthing.ConfiguredFolder, epoch uint64) (firstSyncMarker, bool, error) {
	var marker firstSyncMarker
	present, err := readBoundedJSON(filepath.Join(firstSyncKindRoot(card, folder.Kind), firstSyncMarkerName), &marker)
	if err != nil || !present {
		return firstSyncMarker{}, false, err
	}
	if marker.Schema != firstSyncDocumentSchema || marker.State != "complete" || marker.Epoch != epoch || epoch == 0 || marker.FolderID != folder.ID ||
		marker.CardID != card.Identity.ID || marker.Kind != folder.Kind || !marker.HubVersioningAcknowledged ||
		!marker.ExplicitStart || marker.CompletedAt == "" {
		return firstSyncMarker{}, false, nil
	}
	switch marker.Mode {
	case "sendonly":
		if folder.Type != "sendonly" || marker.FolderType != "sendonly" || marker.SnapshotName != "" {
			return firstSyncMarker{}, false, nil
		}
	case "snapshot":
		if folder.Type == "sendonly" || marker.FolderType == "sendonly" ||
			(marker.FolderType != "sendreceive" && marker.FolderType != "receiveonly") || marker.SnapshotName == "" {
			return firstSyncMarker{}, false, nil
		}
		header, ok, err := readSnapshotHeader(filepath.Join(firstSyncKindRoot(card, folder.Kind), marker.SnapshotName), folder, card, epoch)
		if err != nil || !ok || header.Name != marker.SnapshotName {
			return firstSyncMarker{}, false, err
		}
	default:
		return firstSyncMarker{}, false, nil
	}
	return marker, true, nil
}

func validateFirstSyncBinding(folder syncthing.ConfiguredFolder, card cards.Card, binding folderControlRecord) error {
	if binding.CardID != card.Identity.ID || binding.Kind != folder.Kind || binding.MarkerName != folder.MarkerName ||
		filepath.Clean(folder.Path) != filepath.Clean(managedContentPath(card.Source, folder.Kind)) {
		return errors.New("first-sync folder binding does not match its physical card")
	}
	if err := cards.ValidateManagedMarker(folder.Path, folder.MarkerName); err != nil {
		return err
	}
	if folder.Type != "sendonly" {
		expectedVersions := filepath.Join(card.Source.UserdataPath, leaf.AppStateName, "versions", folder.Kind)
		if folder.VersioningType != "simple" || folder.VersioningFSType != "basic" ||
			filepath.Clean(folder.VersioningFSPath) != filepath.Clean(expectedVersions) {
			return errors.New("first-sync receive folder does not have required same-card Simple Versioning")
		}
	}
	return nil
}

func cardForConfiguredFolder(folder syncthing.ConfiguredFolder, inventory []cards.Card, controlState map[string]folderControlRecord) (cards.Card, bool) {
	binding, ok := controlState[folder.ID]
	if !ok || !completeFolderBinding(binding) || binding.Kind != folder.Kind {
		return cards.Card{}, false
	}
	var found cards.Card
	count := 0
	for _, card := range inventory {
		if card.Identity.ID == binding.CardID {
			found = card
			count++
		}
	}
	return found, count == 1
}

func usableEnrolledCard(card cards.Card) bool {
	return card.Identity.ID != "" && card.State == cards.StateEnrolled && card.Present && card.Writable && !card.DuplicateID
}

func firstSyncKindRoot(card cards.Card, kind string) string {
	return filepath.Join(card.Source.UserdataPath, leaf.AppStateName, "snapshots", kind)
}

func progressFromSnapshot(state string, header snapshotHeader) firstSyncProgress {
	return firstSyncProgress{State: state, SnapshotName: header.Name, FileCount: header.FileCount, DirectoryCount: header.DirectoryCount, ContentBytes: header.ContentBytes}
}

func displayFirstSyncError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

func ensureSafeDirectoryChain(base, target string) error {
	base = filepath.Clean(base)
	target = filepath.Clean(target)
	relative, err := leaf.RelativeWithin(base, target)
	if err != nil || relative == "." {
		return errors.New("owned directory must be a confined child of the card")
	}
	baseInfo, err := os.Lstat(base)
	if err != nil || baseInfo.Mode()&os.ModeSymlink != 0 || !baseInfo.IsDir() {
		return errors.New("card root is absent or unsafe")
	}
	current := base
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("owned directory component is unsafe: %s", current)
		}
	}
	return nil
}

func writeBoundedJSON(path string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if len(payload) > maxFirstSyncDocumentBytes {
		return errors.New("first-sync document exceeds its size limit")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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
	return file.Close()
}

func replaceBoundedJSON(path, temporaryName string, value any) error {
	temporary := filepath.Join(filepath.Dir(path), temporaryName)
	if err := removeSafeRegularIfExists(temporary); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("first-sync document target is unsafe")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := writeBoundedJSON(temporary, value); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func readBoundedJSON(path string, target any) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxFirstSyncDocumentBytes {
		return false, errors.New("first-sync document is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxFirstSyncDocumentBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return false, errors.New("first-sync document contains trailing data")
	}
	return true, nil
}
