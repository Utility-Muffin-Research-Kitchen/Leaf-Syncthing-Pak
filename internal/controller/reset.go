package controller

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
)

const (
	ResetActionIndex     = "index-only"
	ResetActionFull      = "full"
	ResetActionAvailable = "available-only"
	resetPlanName        = "reset-plan.json"
	resetIntentName      = ".leaf-syncthing-reset.json"
	maxResetDocument     = 128 * 1024
)

var (
	ErrResetCardAbsent = errors.New("reset requires every enrolled card to be present")
	ErrResetPending    = errors.New("reset recovery is waiting for required storage")
	indexDirectory     = regexp.MustCompile(`^index-v[0-9.]+\.db$`)
)

type resetRoot struct {
	Path           string `json:"path"`
	FilesystemRoot string `json:"filesystem_root"`
	Description    string `json:"description"`
}

type resetCard struct {
	ID           string `json:"id"`
	SourceID     string `json:"source_id"`
	Root         string `json:"root"`
	UserdataPath string `json:"userdata_path"`
}

type resetDocument struct {
	Schema        int         `json:"schema"`
	ActionID      string      `json:"action_id"`
	Action        string      `json:"action"`
	Roots         []resetRoot `json:"roots"`
	RequiredCards []resetCard `json:"required_cards"`
	Retained      []string    `json:"retained"`
}

type ResetPlanStatus struct {
	ActionID string
	Action   string
	Remove   []string
	Retained []string
}

type ResetOptions struct {
	Inventory      func() ([]cards.Card, error)
	SyncFilesystem func(string) error
	Fault          func(string) error
}

func resetPlanPath(config Config) string { return filepath.Join(config.RuntimeDir, resetPlanName) }
func resetIntentPath(config Config) string {
	return filepath.Join(config.UserdataPath, resetIntentName)
}

func PrepareResetPlan(config Config, inventory []cards.Card, action string) (ResetPlanStatus, error) {
	document, err := buildResetDocument(config, inventory, action)
	if err != nil {
		return ResetPlanStatus{}, err
	}
	random := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return ResetPlanStatus{}, err
	}
	document.ActionID = hex.EncodeToString(random)
	if err := writeResetDocument(resetPlanPath(config), document, syncStateFilesystem, nil); err != nil {
		return ResetPlanStatus{}, err
	}
	return resetPlanStatus(document), nil
}

// ExecuteResetPlan is the post-CTL-1 helper path. The caller must first prove
// the Jawaka service state is stopped; acquiring controller.lock then prevents
// a replacement generation from starting while the sealed plan is converted
// into the durable intent and completed.
func ExecuteResetPlan(config Config, actionID string, options ResetOptions) error {
	if len(actionID) != 32 {
		return errors.New("reset action id is invalid")
	}
	lock, err := acquireControllerLock(config.RuntimeDir)
	if err != nil {
		return fmt.Errorf("prove controller group absence: %w", err)
	}
	defer lock.Close()
	plan, present, err := readResetDocument(resetPlanPath(config))
	if err != nil || !present || plan.ActionID != actionID {
		return errors.New("reset plan is absent, unsafe, or stale")
	}
	inventory, err := resetInventory(config, options)
	if err != nil {
		return err
	}
	rebuilt, err := buildResetDocument(config, inventory, plan.Action)
	if err != nil {
		return err
	}
	rebuilt.ActionID = plan.ActionID
	if !sameResetDocument(plan, rebuilt) {
		return errors.New("storage changed after reset confirmation; prepare the reset again")
	}
	if err := runResetIntent(config, plan, inventory, options); err != nil {
		return err
	}
	_ = removeSafeRegular(resetPlanPath(config))
	return nil
}

// RecoverReset completes a durable intent before any entrypoint may generate,
// migrate, inspect, or start Syncthing state.
func RecoverReset(config Config, options ResetOptions) (bool, error) {
	intentPath := resetIntentPath(config)
	temporary := intentPath + ".tmp"
	intent, present, err := readResetDocument(intentPath)
	if err != nil {
		return false, err
	}
	if !present {
		if err := removeSafeRegularIfExists(temporary); err != nil {
			return false, err
		}
		return false, nil
	}
	inventory, err := resetInventory(config, options)
	if err != nil {
		return true, err
	}
	if err := runResetIntent(config, intent, inventory, options); err != nil {
		return true, err
	}
	return true, nil
}

func buildResetDocument(config Config, inventory []cards.Card, action string) (resetDocument, error) {
	document := resetDocument{Schema: 1, ActionID: strings.Repeat("0", 32), Action: action, Roots: []resetRoot{}, RequiredCards: []resetCard{}, Retained: []string{}}
	primaryState := filepath.Join(config.UserdataPath, leaf.AppStateName)
	addPrimary := func(path, description string) error {
		if err := resetPathWithin(config.UserdataPath, path); err != nil {
			return err
		}
		document.Roots = append(document.Roots, resetRoot{Path: filepath.Clean(path), FilesystemRoot: config.UserdataPath, Description: description})
		return nil
	}
	switch action {
	case ResetActionIndex:
		entries, err := os.ReadDir(config.DataDir)
		if err != nil && !os.IsNotExist(err) {
			return resetDocument{}, err
		}
		for _, entry := range entries {
			if !indexDirectory.MatchString(entry.Name()) {
				continue
			}
			path := filepath.Join(config.DataDir, entry.Name())
			info, infoErr := os.Lstat(path)
			if infoErr != nil {
				return resetDocument{}, infoErr
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return resetDocument{}, errors.New("Syncthing index root is unsafe")
			}
			if err := addPrimary(path, "Syncthing derived index "+entry.Name()); err != nil {
				return resetDocument{}, err
			}
		}
	case ResetActionFull, ResetActionAvailable:
		for _, root := range []struct{ path, description string }{
			{config.ConfigDir, "Syncthing configuration and device identity"},
			{config.DataDir, "Syncthing derived database"},
			{filepath.Join(primaryState, "backups"), "Syncthing configuration backups"},
			{filepath.Join(primaryState, "leaf", "trusted-clients.json"), "trusted browser records"},
			{filepath.Join(primaryState, "leaf", "gateway-cert.pem"), "gateway certificate"},
			{filepath.Join(primaryState, "leaf", "gateway-key.pem"), "gateway private key"},
			{filepath.Join(primaryState, "leaf", folderControlStateName), "managed-folder control state"},
		} {
			if err := addPrimary(root.path, root.description); err != nil {
				return resetDocument{}, err
			}
		}
		for _, card := range enrolledCards(inventory) {
			if !card.Present || card.State != cards.StateEnrolled || card.DuplicateID {
				retained := retainedCardRoots(card)
				if action == ResetActionFull {
					return resetDocument{}, fmt.Errorf("%w: %s", ErrResetCardAbsent, sourceLabel(card.Source))
				}
				document.Retained = append(document.Retained, retained...)
				continue
			}
			required := resetCard{ID: card.Identity.ID, SourceID: card.Source.ID, Root: filepath.Clean(card.Source.Root), UserdataPath: filepath.Clean(card.Source.UserdataPath)}
			document.RequiredCards = append(document.RequiredCards, required)
			for _, root := range retainedCardRoots(card) {
				if err := resetPathWithin(card.Source.UserdataPath, root); err != nil {
					return resetDocument{}, err
				}
				document.Roots = append(document.Roots, resetRoot{Path: root, FilesystemRoot: card.Source.Root, Description: "card snapshots or version history"})
			}
		}
	default:
		return resetDocument{}, errors.New("unsupported reset action")
	}
	sortResetDocument(&document)
	if err := validateResetDocument(config, document); err != nil {
		return resetDocument{}, err
	}
	return document, nil
}

func runResetIntent(config Config, document resetDocument, inventory []cards.Card, options ResetOptions) error {
	if err := validateResetDocument(config, document); err != nil {
		return err
	}
	if err := verifyRequiredCards(document.RequiredCards, inventory); err != nil {
		return err
	}
	syncFilesystem := options.SyncFilesystem
	if syncFilesystem == nil {
		syncFilesystem = syncStateFilesystem
	}
	intentPath := resetIntentPath(config)
	_, present, err := readResetDocument(intentPath)
	if err != nil {
		return err
	}
	if !present {
		if err := resetFault(options, "before-intent"); err != nil {
			return err
		}
		if err := writeResetDocument(intentPath, document, syncFilesystem, options.Fault); err != nil {
			return err
		}
		if err := resetFault(options, "intent-persisted"); err != nil {
			return err
		}
	}
	for index, root := range document.Roots {
		if err := removeDeclaredPath(root.Path); err != nil {
			return fmt.Errorf("remove %s: %w", root.Description, err)
		}
		if err := resetFault(options, fmt.Sprintf("root-removed-%d", index)); err != nil {
			return err
		}
	}
	for _, filesystem := range uniqueFilesystems(document.Roots) {
		if err := syncFilesystem(filesystem); err != nil {
			return err
		}
	}
	for _, root := range document.Roots {
		if _, err := os.Lstat(root.Path); !os.IsNotExist(err) {
			return fmt.Errorf("reset root remains: %s", root.Path)
		}
	}
	if err := resetFault(options, "roots-synced"); err != nil {
		return err
	}
	if err := removeSafeRegular(intentPath); err != nil {
		return err
	}
	if err := resetFault(options, "intent-cleared"); err != nil {
		return err
	}
	return syncFilesystem(config.UserdataPath)
}

func writeResetDocument(path string, document resetDocument, syncFilesystem func(string) error, fault func(string) error) error {
	if !filepath.IsAbs(path) || syncFilesystem == nil {
		return errors.New("reset document path or sync function is invalid")
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	temporary := path + ".tmp"
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
	if fault != nil {
		if err := fault("intent-temporary-synced"); err != nil {
			return err
		}
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return syncFilesystem(filepath.Dir(path))
}

func readResetDocument(path string) (resetDocument, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return resetDocument{}, false, nil
	}
	if err != nil {
		return resetDocument{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxResetDocument {
		return resetDocument{}, true, errors.New("reset document is unsafe")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return resetDocument{}, true, err
	}
	var document resetDocument
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return resetDocument{}, true, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return resetDocument{}, true, errors.New("reset document contains trailing JSON")
	}
	return document, true, nil
}

func validateResetDocument(config Config, document resetDocument) error {
	decodedAction, actionErr := hex.DecodeString(document.ActionID)
	if document.Schema != 1 || len(document.ActionID) != 32 ||
		actionErr != nil || len(decodedAction) != 16 ||
		(document.Action != ResetActionIndex && document.Action != ResetActionFull && document.Action != ResetActionAvailable) ||
		len(document.Roots) > 64 || len(document.RequiredCards) > 16 || len(document.Retained) > 32 {
		return errors.New("reset document schema is unsupported")
	}
	allowedFilesystems := map[string]string{filepath.Clean(config.UserdataPath): filepath.Clean(config.UserdataPath)}
	for _, source := range config.Sources {
		allowedFilesystems[filepath.Clean(source.Root)] = filepath.Clean(source.UserdataPath)
	}
	seen := make(map[string]bool)
	allowedRoots, allowedRetained, err := exactResetPaths(config, document)
	if err != nil {
		return err
	}
	for _, root := range document.Roots {
		base, ok := allowedFilesystems[filepath.Clean(root.FilesystemRoot)]
		if !ok || root.Path == "" || root.Description == "" || seen[root.Path] || resetPathWithin(base, root.Path) != nil ||
			!allowedRoots[filepath.Clean(root.Path)] {
			return errors.New("reset document contains an unsafe or duplicate root")
		}
		seen[root.Path] = true
		if liveContentPath(config.Sources, root.Path) {
			return errors.New("reset document attempted to include live content")
		}
	}
	retainedSeen := map[string]bool{}
	for _, retained := range document.Retained {
		retained = filepath.Clean(retained)
		if retainedSeen[retained] || !allowedRetained[retained] {
			return errors.New("reset document contains an unsafe retained root")
		}
		retainedSeen[retained] = true
	}
	return nil
}

func exactResetPaths(config Config, document resetDocument) (map[string]bool, map[string]bool, error) {
	allowed := map[string]bool{}
	retained := map[string]bool{}
	primaryState := filepath.Join(config.UserdataPath, leaf.AppStateName)
	if document.Action == ResetActionIndex {
		for _, root := range document.Roots {
			if filepath.Clean(filepath.Dir(root.Path)) != filepath.Clean(config.DataDir) || !indexDirectory.MatchString(filepath.Base(root.Path)) {
				return nil, nil, errors.New("index reset contains a non-index root")
			}
			allowed[filepath.Clean(root.Path)] = true
		}
		if len(document.RequiredCards) != 0 || len(document.Retained) != 0 {
			return nil, nil, errors.New("index reset unexpectedly names card state")
		}
		return allowed, retained, nil
	}
	for _, path := range []string{
		config.ConfigDir, config.DataDir, filepath.Join(primaryState, "backups"),
		filepath.Join(primaryState, "leaf", "trusted-clients.json"),
		filepath.Join(primaryState, "leaf", "gateway-cert.pem"),
		filepath.Join(primaryState, "leaf", "gateway-key.pem"),
		filepath.Join(primaryState, "leaf", folderControlStateName),
	} {
		allowed[filepath.Clean(path)] = true
	}
	sources := map[string]leaf.Source{}
	for _, source := range config.Sources {
		sources[source.ID] = source
		state := filepath.Join(source.UserdataPath, leaf.AppStateName)
		retained[filepath.Join(state, "snapshots")] = true
		retained[filepath.Join(state, "versions")] = true
	}
	cardSeen := map[string]bool{}
	for _, card := range document.RequiredCards {
		source, ok := sources[card.SourceID]
		decoded, decodeErr := hex.DecodeString(card.ID)
		if !ok || decodeErr != nil || len(decoded) != 16 || cardSeen[card.ID] ||
			filepath.Clean(source.Root) != filepath.Clean(card.Root) ||
			filepath.Clean(source.UserdataPath) != filepath.Clean(card.UserdataPath) {
			return nil, nil, errors.New("reset document contains an invalid required card")
		}
		cardSeen[card.ID] = true
		state := filepath.Join(source.UserdataPath, leaf.AppStateName)
		allowed[filepath.Join(state, "snapshots")] = true
		allowed[filepath.Join(state, "versions")] = true
		delete(retained, filepath.Join(state, "snapshots"))
		delete(retained, filepath.Join(state, "versions"))
	}
	if document.Action == ResetActionFull && len(document.Retained) != 0 {
		return nil, nil, errors.New("full reset cannot retain declared card roots")
	}
	return allowed, retained, nil
}

func verifyRequiredCards(required []resetCard, inventory []cards.Card) error {
	for _, wanted := range required {
		found := false
		for _, card := range inventory {
			if card.Identity.ID == wanted.ID && card.Source.ID == wanted.SourceID && card.State == cards.StateEnrolled &&
				card.Present && !card.DuplicateID && filepath.Clean(card.Source.Root) == wanted.Root &&
				filepath.Clean(card.Source.UserdataPath) == wanted.UserdataPath {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: card ...%s", ErrResetPending, identitySuffix(wanted.ID))
		}
	}
	return nil
}

func resetInventory(config Config, options ResetOptions) ([]cards.Card, error) {
	if options.Inventory != nil {
		return options.Inventory()
	}
	live, err := cards.InspectSources(config.Sources, cards.Options{})
	if err != nil {
		return nil, err
	}
	registry := filepath.Join(config.UserdataPath, leaf.AppStateName, "leaf")
	return cards.ReconcileRegistry(registry, config.Sources, live, nil)
}

func enrolledCards(inventory []cards.Card) []cards.Card {
	result := []cards.Card{}
	for _, card := range inventory {
		if card.Identity.ID != "" {
			result = append(result, card)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Identity.ID < result[right].Identity.ID })
	return result
}

func retainedCardRoots(card cards.Card) []string {
	state := filepath.Join(card.Source.UserdataPath, leaf.AppStateName)
	return []string{filepath.Join(state, "snapshots"), filepath.Join(state, "versions")}
}

func resetPathWithin(base, target string) error {
	base, target = filepath.Clean(base), filepath.Clean(target)
	if base == target {
		return errors.New("reset root cannot be a mount or userdata root")
	}
	_, err := leaf.RelativeWithin(base, target)
	return err
}

func liveContentPath(sources leaf.SourceList, target string) bool {
	target = filepath.Clean(target)
	for _, source := range sources {
		for _, live := range []string{source.SavesPath, source.StatesPath, source.RomsPath} {
			if live != "" && filepath.Clean(live) == target {
				return true
			}
		}
	}
	return false
}

func removeDeclaredPath(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("declared reset root is a symlink")
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return errors.New("declared reset root has an unsupported type")
		}
		return os.Remove(path)
	}
	paths := []string{}
	err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		paths = append(paths, current)
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(paths) - 1; index >= 0; index-- {
		if err := os.Remove(paths[index]); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func removeSafeRegularIfExists(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	return removeSafeRegular(path)
}

func removeSafeRegular(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("refuse to remove unsafe reset state file")
	}
	return os.Remove(path)
}

func resetPlanStatus(document resetDocument) ResetPlanStatus {
	status := ResetPlanStatus{ActionID: document.ActionID, Action: document.Action, Retained: append([]string(nil), document.Retained...)}
	for _, root := range document.Roots {
		status.Remove = append(status.Remove, root.Description+": "+root.Path)
	}
	return status
}

func sortResetDocument(document *resetDocument) {
	sort.Slice(document.Roots, func(left, right int) bool { return document.Roots[left].Path < document.Roots[right].Path })
	sort.Slice(document.RequiredCards, func(left, right int) bool { return document.RequiredCards[left].ID < document.RequiredCards[right].ID })
	sort.Strings(document.Retained)
}

func sameResetDocument(left, right resetDocument) bool {
	leftPayload, _ := json.Marshal(left)
	rightPayload, _ := json.Marshal(right)
	return bytes.Equal(leftPayload, rightPayload)
}

func uniqueFilesystems(roots []resetRoot) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, root := range roots {
		filesystem := filepath.Clean(root.FilesystemRoot)
		if !seen[filesystem] {
			seen[filesystem] = true
			result = append(result, filesystem)
		}
	}
	sort.Strings(result)
	return result
}

func resetFault(options ResetOptions, boundary string) error {
	if options.Fault == nil {
		return nil
	}
	return options.Fault(boundary)
}

func cleanResetError(err error) string {
	if errors.Is(err, ErrResetPending) || errors.Is(err, ErrResetCardAbsent) {
		return err.Error()
	}
	return strings.TrimSpace(err.Error())
}
