// Package cards owns Leaf Syncthing's physical-card identity and verification.
package cards

import (
	"bytes"
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
	"strings"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
)

const (
	IdentityVersion  = 1
	IdentityFileName = "card-id"
	TemporaryName    = "card-id.tmp"
	MaxIdentityBytes = 1024
)

type State string

const (
	StateAbsent     State = "absent"
	StateUnenrolled State = "unenrolled"
	StateEnrolled   State = "enrolled"
	StateInvalid    State = "invalid"
	StateDuplicate  State = "duplicate"
)

var (
	ErrUnavailable     = errors.New("card is not mounted")
	ErrReadOnly        = errors.New("card is mounted read-only")
	ErrInvalidIdentity = errors.New("card-id is invalid")
	ErrMarkerMissing   = errors.New("managed folder marker is missing")
	ErrMarkerCollision = errors.New("managed folder marker collides with a non-directory entry")
	ErrForeignMarker   = errors.New("default .stfolder indicates a foreign Syncthing manager")
	ErrUnsafeRoot      = errors.New("managed folder root is absent, symlinked, or not a directory")
)

type Identity struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
}

// BindingNames derives the default network ID and mandatory local marker for
// one (card, kind) binding. Adopted network folders keep their existing ID.
func BindingNames(cardID, kind string) (folderID, markerName string, err error) {
	if !validIdentityID(cardID) {
		return "", "", ErrInvalidIdentity
	}
	if kind != "saves" && kind != "states" {
		return "", "", fmt.Errorf("unsupported managed folder kind %q", kind)
	}
	digest := sha256.Sum256([]byte(cardID + kind))
	hexDigest := hex.EncodeToString(digest[:])
	return "leaf-" + kind + "-" + hexDigest[:16], ".leaf-" + kind + "-" + hexDigest[:12], nil
}

func validIdentityID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return len(id) == 32 && err == nil && len(decoded) == 16 && id == strings.ToLower(id)
}

// ValidateManagedMarker refuses a default foreign marker and requires the
// binding-specific marker to exist as a real directory.
func ValidateManagedMarker(root, markerName string) error {
	if markerName == "" || markerName == ".stfolder" || filepath.Base(markerName) != markerName {
		return ErrMarkerCollision
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return ErrUnsafeRoot
	}
	if _, err := os.Lstat(filepath.Join(root, ".stfolder")); err == nil {
		return ErrForeignMarker
	} else if !os.IsNotExist(err) {
		return err
	}
	info, err := os.Lstat(filepath.Join(root, markerName))
	if os.IsNotExist(err) {
		return ErrMarkerMissing
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrMarkerCollision
	}
	return nil
}

type Issue struct {
	Code    string
	Message string
}

type Card struct {
	Source        leaf.Source
	Identity      Identity
	State         State
	Present       bool
	Writable      bool
	DuplicateID   bool
	RetainedBytes int64
	Issues        []Issue
}

type Options struct {
	MountInfo      []byte
	Random         io.Reader
	SyncFilesystem func(string) error
}

// InspectSources recovers abandoned enrollment temporaries, verifies each
// configured PATH-2 source against decoded mountinfo, and detects cloned IDs.
func InspectSources(sources leaf.SourceList, options Options) ([]Card, error) {
	mountInfo, err := options.mountInfo()
	if err != nil {
		return nil, err
	}
	if options.SyncFilesystem == nil {
		options.SyncFilesystem = syncFilesystemAt
	}
	mounts := parseMountInfo(mountInfo)
	cards := make([]Card, len(sources))
	for index, source := range sources {
		cards[index], err = inspectSource(source, mounts, options.SyncFilesystem)
		if err != nil {
			return nil, fmt.Errorf("inspect source %s: %w", source.ID, err)
		}
	}

	counts := make(map[string]int)
	for _, card := range cards {
		if card.Identity.ID != "" {
			counts[card.Identity.ID]++
		}
	}
	for index := range cards {
		if cards[index].Identity.ID != "" && counts[cards[index].Identity.ID] > 1 {
			cards[index].State = StateDuplicate
			cards[index].DuplicateID = true
			cards[index].Issues = append(cards[index].Issues, Issue{
				Code: "duplicate-card-id", Message: "Another mounted card presents the same Leaf Syncthing identity",
			})
		}
	}
	return cards, nil
}

// Enroll creates a random 128-bit identity only after an explicit caller asks
// for it. Existing valid identities are returned unchanged.
func Enroll(source leaf.Source, options Options) (Identity, bool, error) {
	mountInfo, err := options.mountInfo()
	if err != nil {
		return Identity{}, false, err
	}
	mount, present := parseMountInfo(mountInfo)[filepath.Clean(source.Root)]
	if !present {
		return Identity{}, false, ErrUnavailable
	}
	if !mount.writable {
		return Identity{}, false, ErrReadOnly
	}
	if err := validateSource(source); err != nil {
		return Identity{}, false, err
	}
	if options.SyncFilesystem == nil {
		options.SyncFilesystem = syncFilesystemAt
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}

	stateRoot := filepath.Join(source.UserdataPath, leaf.AppStateName)
	if err := ensureDirectoryWithin(source.Root, stateRoot, 0o700); err != nil {
		return Identity{}, false, fmt.Errorf("create card state root: %w", err)
	}
	identityPath := filepath.Join(stateRoot, IdentityFileName)
	temporaryPath := filepath.Join(stateRoot, TemporaryName)

	existing, exists, err := readIdentity(identityPath)
	if err != nil {
		return Identity{}, false, err
	}
	if exists {
		if err := discardTemporary(temporaryPath, source.Root, options.SyncFilesystem); err != nil {
			return Identity{}, false, err
		}
		if err := options.SyncFilesystem(source.Root); err != nil {
			return Identity{}, false, fmt.Errorf("confirm enrolled card filesystem: %w", err)
		}
		return existing, false, nil
	}
	if err := discardTemporary(temporaryPath, source.Root, options.SyncFilesystem); err != nil {
		return Identity{}, false, err
	}

	random := make([]byte, 16)
	if _, err := io.ReadFull(options.Random, random); err != nil {
		return Identity{}, false, fmt.Errorf("generate card identity: %w", err)
	}
	identity := Identity{Version: IdentityVersion, ID: hex.EncodeToString(random)}
	payload, err := json.Marshal(identity)
	if err != nil {
		return Identity{}, false, err
	}
	payload = append(payload, '\n')
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Identity{}, false, fmt.Errorf("create card-id temporary: %w", err)
	}
	if _, err := io.Copy(file, bytes.NewReader(payload)); err != nil {
		_ = file.Close()
		return Identity{}, false, fmt.Errorf("write card-id temporary: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return Identity{}, false, fmt.Errorf("flush card-id temporary: %w", err)
	}
	if err := file.Close(); err != nil {
		return Identity{}, false, fmt.Errorf("close card-id temporary: %w", err)
	}
	if _, exists, err := readIdentity(identityPath); err != nil {
		return Identity{}, false, fmt.Errorf("recheck card-id before promotion: %w", err)
	} else if exists {
		return Identity{}, false, errors.New("card-id appeared before promotion")
	}
	if err := os.Rename(temporaryPath, identityPath); err != nil {
		return Identity{}, false, fmt.Errorf("promote card-id: %w", err)
	}
	if err := options.SyncFilesystem(source.Root); err != nil {
		return Identity{}, false, fmt.Errorf("flush enrolled card filesystem: %w", err)
	}
	return identity, true, nil
}

func inspectSource(source leaf.Source, mounts map[string]mountRecord, syncFilesystem func(string) error) (Card, error) {
	card := Card{Source: source, State: StateAbsent, Issues: []Issue{}}
	mount, present := mounts[filepath.Clean(source.Root)]
	if !present {
		card.Issues = append(card.Issues, Issue{Code: "card-absent", Message: "The configured card is not mounted"})
		return card, nil
	}
	card.Present = true
	card.Writable = mount.writable
	if err := validateSource(source); err != nil {
		card.State = StateInvalid
		card.Issues = append(card.Issues, Issue{Code: "unsafe-card-path", Message: err.Error()})
		return card, nil
	}

	stateRoot := filepath.Join(source.UserdataPath, leaf.AppStateName)
	info, err := os.Lstat(stateRoot)
	if os.IsNotExist(err) {
		card.State = StateUnenrolled
		return card, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		card.State = StateInvalid
		card.Issues = append(card.Issues, Issue{Code: "unsafe-card-state", Message: "Card state root is not a real directory"})
		return card, nil
	}
	if err := discardTemporary(filepath.Join(stateRoot, TemporaryName), source.Root, syncFilesystem); err != nil {
		return Card{}, err
	}
	identity, exists, err := readIdentity(filepath.Join(stateRoot, IdentityFileName))
	if err != nil {
		card.State = StateInvalid
		card.Issues = append(card.Issues, Issue{Code: "invalid-card-id", Message: "The card identity is invalid and was not replaced"})
	} else if !exists {
		card.State = StateUnenrolled
	} else {
		card.State = StateEnrolled
		card.Identity = identity
		if err := syncFilesystem(source.Root); err != nil {
			return Card{}, fmt.Errorf("confirm enrolled card filesystem: %w", err)
		}
	}
	retainedBytes, retainedErr := retainedSize(stateRoot)
	if retainedErr != nil {
		card.State = StateInvalid
		card.Issues = append(card.Issues, Issue{Code: "unsafe-retained-state", Message: retainedErr.Error()})
	} else {
		card.RetainedBytes = retainedBytes
	}
	return card, nil
}

func validateSource(source leaf.Source) error {
	if source.ID == "" || source.Root == "" || source.UserdataPath == "" {
		return errors.New("source id, root, and PATH-2 userdata are required")
	}
	root, err := os.Lstat(source.Root)
	if err != nil {
		return fmt.Errorf("inspect card root: %w", err)
	}
	if root.Mode()&os.ModeSymlink != 0 || !root.IsDir() {
		return errors.New("card root is not a real directory")
	}
	if _, err := leaf.RelativeWithin(source.Root, source.UserdataPath); err != nil {
		return fmt.Errorf("userdata is outside card: %w", err)
	}
	return nil
}

func readIdentity(path string) (Identity, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Identity{}, false, nil
	}
	if err != nil {
		return Identity{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > MaxIdentityBytes {
		return Identity{}, true, ErrInvalidIdentity
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, true, err
	}
	var identity Identity
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil {
		return Identity{}, true, ErrInvalidIdentity
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Identity{}, true, ErrInvalidIdentity
	}
	if identity.Version != IdentityVersion || len(identity.ID) != 32 {
		return Identity{}, true, ErrInvalidIdentity
	}
	if !validIdentityID(identity.ID) {
		return Identity{}, true, ErrInvalidIdentity
	}
	return identity, true, nil
}

func discardTemporary(path, filesystemRoot string, syncFilesystem func(string) error) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("card-id temporary is not a real file")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("discard card-id temporary: %w", err)
	}
	if err := syncFilesystem(filesystemRoot); err != nil {
		return fmt.Errorf("flush card-id temporary recovery: %w", err)
	}
	return nil
}

func ensureDirectoryWithin(root, target string, mode os.FileMode) error {
	relative, err := leaf.RelativeWithin(root, target)
	if err != nil {
		return err
	}
	current := filepath.Clean(root)
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, mode); err != nil {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("path component %s is not a real directory", current)
		}
	}
	return nil
}

func retainedSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("retained state contains symlink %s", path)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > int64(^uint64(0)>>1)-total {
			return errors.New("retained state size overflows")
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func (options Options) mountInfo() ([]byte, error) {
	if options.MountInfo != nil {
		return options.MountInfo, nil
	}
	contents, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("read mountinfo: %w", err)
	}
	return contents, nil
}
