package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

const (
	onboardingPlanLifetime = 5 * time.Minute
	maxOnboardingPlans     = 16
)

type managedFolderUpstream interface {
	ConfiguredFolderDevices(context.Context, string) ([]string, error)
	AddManagedFolder(context.Context, syncthing.ConfiguredFolder) error
}

type b3FolderUpstream interface {
	managedFolderUpstream
	SetManagedFolderType(context.Context, syncthing.ConfiguredFolder) error
	SetManagedFolderDevices(context.Context, syncthing.ConfiguredFolder) error
	RemoveManagedFolder(context.Context, string) error
	SetFolderPaused(context.Context, string, bool) error
	RescanFolder(context.Context, string) error
}

type onboardingPlan struct {
	ID               string
	SourceID         string
	CardID           string
	Kind             string
	FolderType       string
	FolderID         string
	Label            string
	Path             string
	MarkerName       string
	VersioningPath   string
	FileCount        int
	DirectoryCount   int
	ContentBytes     int64
	AvailableBytes   uint64
	SnapshotPossible bool
	PeerCount        int
	StatesWarning    bool
	OfferDeviceID    string
	Devices          []string
	ExpiresAt        time.Time
}

type onboardingOptions struct {
	Now            func() time.Time
	Random         io.Reader
	AvailableBytes func(string) (uint64, error)
	SyncFilesystem func(string) error
}

type onboardingManager struct {
	mu      sync.Mutex
	options onboardingOptions
	plans   map[string]onboardingPlan
}

func newOnboardingManager(options onboardingOptions) *onboardingManager {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.AvailableBytes == nil {
		options.AvailableBytes = leaf.AvailableBytes
	}
	if options.SyncFilesystem == nil {
		options.SyncFilesystem = syncStateFilesystem
	}
	return &onboardingManager{options: options, plans: make(map[string]onboardingPlan)}
}

func (manager *onboardingManager) Plan(ctx context.Context, sourceID, kind, folderType, selfDeviceID string, deviceIDs []string, inventory []cards.Card, configured []syncthing.ConfiguredFolder, upstream managedFolderUpstream) (onboardingPlan, error) {
	return manager.plan(ctx, sourceID, kind, folderType, "", "", "", selfDeviceID, deviceIDs, inventory, configured, upstream)
}

func (manager *onboardingManager) PlanOffer(ctx context.Context, sourceID, kind, folderType, folderID, label, offerDeviceID, selfDeviceID string, inventory []cards.Card, configured []syncthing.ConfiguredFolder, upstream managedFolderUpstream) (onboardingPlan, error) {
	if !syncthing.ValidFolderID(folderID) {
		return onboardingPlan{}, errors.New("the offered network folder id is unsupported")
	}
	normalizedDeviceID, err := syncthing.NormalizeDeviceID(offerDeviceID)
	if err != nil || normalizedDeviceID == selfDeviceID {
		return onboardingPlan{}, errors.New("the offering device is invalid")
	}
	label = strings.TrimSpace(label)
	if !validOnboardingLabel(label) {
		label = "Leaf " + folderKindLabel(kind)
	}
	return manager.plan(ctx, sourceID, kind, folderType, folderID, label, normalizedDeviceID, selfDeviceID, []string{normalizedDeviceID}, inventory, configured, upstream)
}

func (manager *onboardingManager) plan(ctx context.Context, sourceID, kind, folderType, offeredFolderID, offeredLabel, offerDeviceID, selfDeviceID string, deviceIDs []string, inventory []cards.Card, configured []syncthing.ConfiguredFolder, upstream managedFolderUpstream) (onboardingPlan, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked()
	if len(manager.plans) >= maxOnboardingPlans {
		return onboardingPlan{}, errors.New("too many folder setup plans are pending")
	}
	card, err := onboardingCard(sourceID, inventory)
	if err != nil {
		return onboardingPlan{}, err
	}
	if err := validateOnboardingSelection(card, kind, folderType, configured); err != nil {
		return onboardingPlan{}, err
	}
	folderID, markerName, err := cards.BindingNames(card.Identity.ID, kind)
	if err != nil {
		return onboardingPlan{}, err
	}
	label := "Leaf " + folderKindLabel(kind)
	if offeredFolderID != "" {
		folderID = offeredFolderID
		label = offeredLabel
	}
	for _, folder := range configured {
		if folder.ID == folderID {
			return onboardingPlan{}, errors.New("this network folder is already configured")
		}
	}
	path := managedContentPath(card.Source, kind)
	files, directories, contentBytes, err := inspectOnboardingRoot(path, markerName)
	if err != nil {
		return onboardingPlan{}, err
	}
	available, err := manager.options.AvailableBytes(card.Source.Root)
	if err != nil {
		return onboardingPlan{}, err
	}
	devices, err := selectedFolderDevices(ctx, selfDeviceID, deviceIDs, upstream)
	if err != nil {
		return onboardingPlan{}, err
	}
	random := make([]byte, 16)
	if _, err := io.ReadFull(manager.options.Random, random); err != nil {
		return onboardingPlan{}, err
	}
	plan := onboardingPlan{
		ID: hex.EncodeToString(random), SourceID: sourceID, CardID: card.Identity.ID,
		Kind: kind, FolderType: folderType, FolderID: folderID, Label: label,
		Path: path, MarkerName: markerName,
		VersioningPath: filepath.Join(card.Source.UserdataPath, leaf.AppStateName, "versions", kind),
		FileCount:      files, DirectoryCount: directories, ContentBytes: contentBytes, AvailableBytes: available,
		SnapshotPossible: snapshotFits(contentBytes, available), PeerCount: len(devices) - 1,
		StatesWarning: kind == "states", OfferDeviceID: offerDeviceID,
		Devices:   append([]string(nil), devices...),
		ExpiresAt: manager.options.Now().Add(onboardingPlanLifetime),
	}
	manager.plans[plan.ID] = plan
	return plan, nil
}

func (manager *onboardingManager) Create(ctx context.Context, planID, selfDeviceID string, statesWarningAcknowledged, manualEditAcknowledged bool, inventory []cards.Card, configured []syncthing.ConfiguredFolder, controls *folderControlStore, upstream managedFolderUpstream) (syncthing.ConfiguredFolder, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked()
	plan, ok := manager.plans[planID]
	if !ok {
		return syncthing.ConfiguredFolder{}, errors.New("folder setup plan is absent or expired")
	}
	if !manualEditAcknowledged || (plan.StatesWarning && !statesWarningAcknowledged) {
		return syncthing.ConfiguredFolder{}, errors.New("folder setup warnings must be acknowledged")
	}
	card, err := onboardingCard(plan.SourceID, inventory)
	if err != nil {
		return syncthing.ConfiguredFolder{}, err
	}
	if card.Identity.ID != plan.CardID {
		return syncthing.ConfiguredFolder{}, errors.New("the physical card changed after folder setup was reviewed")
	}
	if err := validateOnboardingSelection(card, plan.Kind, plan.FolderType, configured); err != nil {
		return syncthing.ConfiguredFolder{}, err
	}
	folderID, markerName, err := cards.BindingNames(card.Identity.ID, plan.Kind)
	if err != nil || (plan.OfferDeviceID == "" && folderID != plan.FolderID) ||
		(plan.OfferDeviceID != "" && !syncthing.ValidFolderID(plan.FolderID)) || markerName != plan.MarkerName ||
		filepath.Clean(managedContentPath(card.Source, plan.Kind)) != filepath.Clean(plan.Path) {
		return syncthing.ConfiguredFolder{}, errors.New("folder setup binding changed after review")
	}
	_, _, currentContentBytes, err := inspectOnboardingRoot(plan.Path, plan.MarkerName)
	if err != nil {
		return syncthing.ConfiguredFolder{}, err
	}
	if plan.FolderType != "sendonly" {
		currentAvailable, err := manager.options.AvailableBytes(card.Source.Root)
		if err != nil {
			return syncthing.ConfiguredFolder{}, err
		}
		if !snapshotFits(currentContentBytes, currentAvailable) {
			return syncthing.ConfiguredFolder{}, errors.New("insufficient free space for a same-card safety snapshot")
		}
	}
	if len(plan.Devices) < 2 {
		return syncthing.ConfiguredFolder{}, errors.New("folder setup plan has no selected peers")
	}
	devices, err := selectedFolderDevices(ctx, selfDeviceID, plan.Devices[1:], upstream)
	if err != nil {
		return syncthing.ConfiguredFolder{}, err
	}
	if err := prepareOnboardingStorage(card, plan.Kind, plan.FolderType, plan.MarkerName, manager.options.SyncFilesystem); err != nil {
		return syncthing.ConfiguredFolder{}, err
	}
	folder := syncthing.ConfiguredFolder{
		ID: plan.FolderID, Label: plan.Label, Kind: plan.Kind, Path: plan.Path,
		Type: plan.FolderType, MarkerName: plan.MarkerName, Paused: true, Devices: devices,
	}
	if folder.Type != "sendonly" {
		folder.VersioningType = "simple"
		folder.VersioningFSPath = plan.VersioningPath
		folder.VersioningFSType = "basic"
	}
	binding, err := newFolderControlRecord(folder, card)
	if err != nil {
		return syncthing.ConfiguredFolder{}, err
	}
	if err := validateFirstSyncBinding(folder, card, binding); err != nil {
		return syncthing.ConfiguredFolder{}, err
	}
	if err := controls.BeginAdd(folder, card); err != nil {
		return syncthing.ConfiguredFolder{}, err
	}
	if err := upstream.AddManagedFolder(ctx, folder); err != nil {
		if rollbackErr := controls.Remove(folder.ID); rollbackErr != nil {
			return syncthing.ConfiguredFolder{}, errors.New("upstream rejected the folder and control-state rollback failed")
		}
		return syncthing.ConfiguredFolder{}, err
	}
	if err := controls.Activate(folder.ID); err != nil {
		return syncthing.ConfiguredFolder{}, errors.New("the upstream folder was added but its durable binding is still pending recovery")
	}
	delete(manager.plans, plan.ID)
	return folder, nil
}

func selectedFolderDevices(ctx context.Context, selfDeviceID string, selected []string, upstream managedFolderUpstream) ([]string, error) {
	self, err := syncthing.NormalizeDeviceID(selfDeviceID)
	if err != nil {
		return nil, errors.New("the local Syncthing device identity is invalid")
	}
	if len(selected) == 0 || len(selected) > 32 {
		return nil, errors.New("select at least one and at most 32 configured peers")
	}
	configured, err := upstream.ConfiguredFolderDevices(ctx, self)
	if err != nil {
		return nil, err
	}
	available := make(map[string]bool, len(configured))
	for _, rawDeviceID := range configured {
		deviceID, normalizeErr := syncthing.NormalizeDeviceID(rawDeviceID)
		if normalizeErr != nil {
			return nil, errors.New("the configured Syncthing device list is invalid")
		}
		available[deviceID] = true
	}
	if !available[self] {
		return nil, errors.New("the configured Syncthing device list does not contain this device")
	}
	peers := make([]string, 0, len(selected))
	seen := make(map[string]bool, len(selected))
	for _, rawDeviceID := range selected {
		deviceID, normalizeErr := syncthing.NormalizeDeviceID(rawDeviceID)
		if normalizeErr != nil || deviceID == self || seen[deviceID] || !available[deviceID] {
			return nil, errors.New("selected peers must be unique configured Syncthing devices")
		}
		seen[deviceID] = true
		peers = append(peers, deviceID)
	}
	sort.Strings(peers)
	return append([]string{self}, peers...), nil
}

func (manager *onboardingManager) expireLocked() {
	now := manager.options.Now()
	for id, plan := range manager.plans {
		if !now.Before(plan.ExpiresAt) {
			delete(manager.plans, id)
		}
	}
}

func onboardingCard(sourceID string, inventory []cards.Card) (cards.Card, error) {
	var result cards.Card
	count := 0
	for _, card := range inventory {
		if card.Source.ID == sourceID {
			result = card
			count++
		}
	}
	if count != 1 || !usableEnrolledCard(result) {
		return cards.Card{}, errors.New("folder setup requires the selected enrolled physical card to be present and writable")
	}
	return result, nil
}

func validateOnboardingSelection(card cards.Card, kind, folderType string, configured []syncthing.ConfiguredFolder) error {
	if kind != "saves" && kind != "states" {
		return errors.New("Leaf v1 supports only Saves and States folders")
	}
	// The B3 D-11 audit in Jawaka/docs/launch-exit-barriers.md proves every
	// routine emulator path that writes the shared Saves and States trees. ROMs
	// and app userdata remain intentionally absent from this selection surface.
	if managedContentPath(card.Source, kind) == "" {
		return errors.New("the selected source does not publish an eligible PATH-2 content tree")
	}
	if folderType != "sendonly" && folderType != "sendreceive" && folderType != "receiveonly" {
		return errors.New("folder setup type is unsupported")
	}
	path := managedContentPath(card.Source, kind)
	for _, folder := range configured {
		if filepath.Clean(folder.Path) == filepath.Clean(path) {
			return errors.New("this card already has a managed folder for the selected content")
		}
	}
	if _, err := leaf.RelativeWithin(card.Source.Root, path); err != nil || filepath.Clean(path) == filepath.Clean(card.Source.Root) {
		return errors.New("the selected PATH-2 content tree is not confined to the card")
	}
	return nil
}

func validOnboardingLabel(label string) bool {
	if label == "" || len(label) > 96 {
		return false
	}
	for _, character := range label {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func inspectOnboardingRoot(path, markerName string) (int, int, int64, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return 0, 0, 0, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return 0, 0, 0, errors.New("the candidate folder root is absent, symlinked, or not a directory")
	}
	if _, err := os.Lstat(filepath.Join(path, ".stfolder")); err == nil {
		return 0, 0, 0, cards.ErrForeignMarker
	} else if !os.IsNotExist(err) {
		return 0, 0, 0, err
	}
	if marker, err := os.Lstat(filepath.Join(path, markerName)); err == nil {
		if marker.Mode()&os.ModeSymlink != 0 || !marker.IsDir() {
			return 0, 0, 0, cards.ErrMarkerCollision
		}
	} else if !os.IsNotExist(err) {
		return 0, 0, 0, err
	}
	return scanSnapshotSource(path, markerName)
}

func prepareOnboardingStorage(card cards.Card, kind, folderType, markerName string, syncFilesystem func(string) error) error {
	path := managedContentPath(card.Source, kind)
	if err := ensureSafeDirectoryChain(card.Source.Root, path); err != nil {
		return err
	}
	if _, _, _, err := inspectOnboardingRoot(path, markerName); err != nil {
		return err
	}
	markerPath := filepath.Join(path, markerName)
	if info, err := os.Lstat(markerPath); os.IsNotExist(err) {
		if err := os.Mkdir(markerPath, 0o700); err != nil {
			return err
		}
	} else if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return cards.ErrMarkerCollision
	}
	if folderType != "sendonly" {
		versions := filepath.Join(card.Source.UserdataPath, leaf.AppStateName, "versions", kind)
		if err := ensureSafeDirectoryChain(card.Source.Root, versions); err != nil {
			return err
		}
	}
	if err := cards.ValidateManagedMarker(path, markerName); err != nil {
		return err
	}
	return syncFilesystem(card.Source.Root)
}

func snapshotFits(contentBytes int64, available uint64) bool {
	if contentBytes < 0 {
		return false
	}
	required := uint64(contentBytes)
	reserve := uint64(leaf.StorageReserveBytes)
	return required <= ^uint64(0)-reserve && available >= required+reserve
}
