package syncthing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	GenerationMarkerName = ".leaf-generation-v1"
	maxIdentityFileBytes = 1024 * 1024
)

var (
	ErrMigrationRequired = errors.New("syncthing identity: existing config requires an explicit migration")
	deviceIDPattern      = regexp.MustCompile(`^[A-Z2-7]{7}(?:-[A-Z2-7]{7}){7}$`)
)

type IdentityOptions struct {
	Binary          string
	ConfigDir       string
	DataDir         string
	UpstreamVersion string
	GUISocket       string
	SyncFilesystem  SyncFilesystemFunc
}

type Identity struct {
	DeviceID        string
	UpstreamVersion string
	ConfigVersion   int
}

type generationMarker struct {
	Schema          int    `json:"schema"`
	UpstreamVersion string `json:"upstream_version"`
	ConfigVersion   int    `json:"config_version"`
	DeviceID        string `json:"device_id"`
	CertificateSHA  string `json:"certificate_sha256"`
	PrivateKeySHA   string `json:"private_key_sha256"`
}

type parsedConfig struct {
	XMLName xml.Name `xml:"configuration"`
	Version int      `xml:"version,attr"`
	GUI     struct {
		Enabled         bool   `xml:"enabled,attr"`
		Address         string `xml:"address"`
		UnixPermissions string `xml:"unixSocketPermissions"`
		APIKey          string `xml:"apikey"`
	} `xml:"gui"`
	Options struct {
		GlobalAnnounce   bool `xml:"globalAnnounceEnabled"`
		LocalAnnounce    bool `xml:"localAnnounceEnabled"`
		Relays           bool `xml:"relaysEnabled"`
		NAT              bool `xml:"natEnabled"`
		URAccepted       int  `xml:"urAccepted"`
		AutoUpgradeHours int  `xml:"autoUpgradeIntervalH"`
		StartBrowser     bool `xml:"startBrowser"`
		CrashReporting   bool `xml:"crashReportingEnabled"`
	} `xml:"options"`
	Devices []struct {
		ID string `xml:"id,attr"`
	} `xml:"device"`
}

// EnsureIdentity completes SYNC-1's factory-clean generation transaction or
// validates a previously promoted controller-generated identity. It never
// generates into, or replaces files inside, an existing config directory.
func EnsureIdentity(ctx context.Context, options IdentityOptions, recovery RecoveryResult) (Identity, error) {
	if err := options.validate(); err != nil {
		return Identity{}, err
	}
	if options.SyncFilesystem == nil {
		options.SyncFilesystem = syncFilesystemAt
	}
	if err := discardMigrationStaging(options); err != nil {
		return Identity{}, err
	}
	switch recovery.State {
	case RecoveryReady:
		return validateOrMigrateIdentity(ctx, options)
	case RecoveryClean:
		return generateIdentity(ctx, options)
	default:
		return Identity{}, fmt.Errorf("syncthing identity: unsupported recovery state %q", recovery.State)
	}
}

func generateIdentity(ctx context.Context, options IdentityOptions) (Identity, error) {
	stateRoot := filepath.Dir(options.ConfigDir)
	if filepath.Dir(options.DataDir) != stateRoot {
		return Identity{}, errors.New("syncthing identity: config and data directories must share one state root")
	}
	if err := requireRealDirectory(stateRoot); err != nil {
		return Identity{}, fmt.Errorf("validate identity state root: %w", err)
	}
	if _, err := os.Lstat(options.ConfigDir); err == nil {
		return Identity{}, errors.New("syncthing identity: factory generation refuses an existing config directory")
	} else if !os.IsNotExist(err) {
		return Identity{}, fmt.Errorf("inspect final config directory: %w", err)
	}

	configTemporary := filepath.Join(stateRoot, "config.generate.tmp")
	dataTemporary := filepath.Join(stateRoot, "data.generate.tmp")
	for _, path := range []string{configTemporary, dataTemporary} {
		if err := removeOwnedTemporary(stateRoot, path); err != nil {
			return Identity{}, err
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			return Identity{}, fmt.Errorf("create generation directory %s: %w", filepath.Base(path), err)
		}
	}

	cleanup := true
	defer func() {
		if cleanup {
			_ = removeOwnedTemporary(stateRoot, configTemporary)
			_ = removeOwnedTemporary(stateRoot, dataTemporary)
		}
	}()

	command := exec.CommandContext(ctx, options.Binary, "generate",
		"--config="+configTemporary,
		"--data="+dataTemporary,
		"--no-port-probing")
	configureChild(command)
	if err := command.Run(); err != nil {
		return Identity{}, fmt.Errorf("run pinned Syncthing generate: %w", err)
	}
	if err := ApplyInitialProfile(filepath.Join(configTemporary, "config.xml"), options.GUISocket); err != nil {
		return Identity{}, fmt.Errorf("apply initial Leaf profile: %w", err)
	}

	identity, marker, err := inspectGeneratedIdentity(ctx, options.Binary, configTemporary, dataTemporary, options.UpstreamVersion, options.GUISocket)
	if err != nil {
		return Identity{}, err
	}
	if err := writeGenerationMarker(filepath.Join(configTemporary, GenerationMarkerName), marker); err != nil {
		return Identity{}, err
	}
	if err := removeOwnedTemporary(stateRoot, dataTemporary); err != nil {
		return Identity{}, err
	}
	if err := options.SyncFilesystem(stateRoot); err != nil {
		return Identity{}, fmt.Errorf("flush generated identity before promotion: %w", err)
	}
	if err := os.Rename(configTemporary, options.ConfigDir); err != nil {
		return Identity{}, fmt.Errorf("promote generated identity: %w", err)
	}
	if err := options.SyncFilesystem(stateRoot); err != nil {
		return Identity{}, fmt.Errorf("flush promoted identity: %w", err)
	}
	cleanup = false
	return identity, nil
}

func validateOrMigrateIdentity(ctx context.Context, options IdentityOptions) (Identity, error) {
	marker, err := readGenerationMarker(filepath.Join(options.ConfigDir, GenerationMarkerName))
	if err != nil {
		return Identity{}, err
	}
	identity, current, err := inspectGeneratedIdentity(ctx, options.Binary, options.ConfigDir, options.DataDir, marker.UpstreamVersion, options.GUISocket)
	if err != nil {
		return Identity{}, err
	}
	if marker.DeviceID != current.DeviceID || marker.CertificateSHA != current.CertificateSHA || marker.PrivateKeySHA != current.PrivateKeySHA {
		return Identity{}, errors.New("syncthing identity: generation marker does not match the current identity")
	}
	if marker.UpstreamVersion == options.UpstreamVersion && marker.ConfigVersion == current.ConfigVersion {
		return Identity{DeviceID: identity.DeviceID, UpstreamVersion: options.UpstreamVersion, ConfigVersion: identity.ConfigVersion}, nil
	}
	return migrateIdentity(ctx, options, marker)
}

func migrateIdentity(ctx context.Context, options IdentityOptions, original generationMarker) (Identity, error) {
	stateRoot := filepath.Dir(options.ConfigDir)
	configTemporary := filepath.Join(stateRoot, "config.migrate.tmp")
	dataTemporary := filepath.Join(stateRoot, "data.migrate.tmp")
	defer func() {
		_ = removeOwnedTemporary(stateRoot, configTemporary)
		_ = removeOwnedTemporary(stateRoot, dataTemporary)
	}()
	for _, path := range []string{configTemporary, dataTemporary} {
		if err := os.Mkdir(path, 0o700); err != nil {
			return Identity{}, fmt.Errorf("create migration directory %s: %w", filepath.Base(path), err)
		}
	}

	for _, file := range []struct {
		name string
		max  int64
	}{
		{name: "config.xml", max: MaxConfigBytes},
		{name: "cert.pem", max: maxIdentityFileBytes},
		{name: "key.pem", max: maxIdentityFileBytes},
	} {
		if err := copyIdentityFile(filepath.Join(options.ConfigDir, file.name), filepath.Join(configTemporary, file.name), file.max); err != nil {
			return Identity{}, fmt.Errorf("stage migration %s: %w", file.name, err)
		}
	}

	command := exec.CommandContext(ctx, options.Binary, "generate",
		"--config="+configTemporary,
		"--data="+dataTemporary,
		"--no-port-probing")
	configureChild(command)
	if err := command.Run(); err != nil {
		return Identity{}, fmt.Errorf("%w: run pinned Syncthing migration: %v", ErrMigrationRequired, err)
	}
	_, migratedMarker, err := inspectGeneratedIdentity(ctx, options.Binary, configTemporary, dataTemporary, options.UpstreamVersion, options.GUISocket)
	if err != nil {
		return Identity{}, fmt.Errorf("validate migrated identity: %w", err)
	}
	if migratedMarker.DeviceID != original.DeviceID || migratedMarker.CertificateSHA != original.CertificateSHA || migratedMarker.PrivateKeySHA != original.PrivateKeySHA {
		return Identity{}, errors.New("syncthing identity: migration changed the certificate, private key, or device id")
	}
	migratedConfig, err := readSafeConfig(filepath.Join(configTemporary, "config.xml"))
	if err != nil {
		return Identity{}, fmt.Errorf("read migrated config: %w", err)
	}
	for _, path := range []string{configTemporary, dataTemporary} {
		if err := removeOwnedTemporary(stateRoot, path); err != nil {
			return Identity{}, err
		}
	}
	expectedConfigSHA := sha256.Sum256(migratedConfig)
	verify := func(path string) error {
		if err := ValidateXML(path); err != nil {
			return err
		}
		contents, err := readSafeConfig(path)
		if err != nil {
			return err
		}
		if sha256.Sum256(contents) != expectedConfigSHA {
			return errors.New("promoted config differs from validated migration output")
		}
		return nil
	}
	if err := promoteConfig(options.ConfigDir, migratedConfig, options.SyncFilesystem, "syncthing identity migration", verify); err != nil {
		return Identity{}, err
	}

	finalIdentity, finalMarker, err := inspectGeneratedIdentity(ctx, options.Binary, options.ConfigDir, options.DataDir, options.UpstreamVersion, options.GUISocket)
	if err != nil || finalMarker != migratedMarker {
		if err == nil {
			err = errors.New("promoted identity differs from validated migration output")
		}
		return Identity{}, restoreBackup(
			filepath.Join(options.ConfigDir, "config.xml"), filepath.Join(options.ConfigDir, "config.xml.tmp"),
			filepath.Join(options.ConfigDir, "config.xml.bak"), options.ConfigDir, options.SyncFilesystem,
			"syncthing identity migration", err,
		)
	}
	if err := replaceGenerationMarker(options.ConfigDir, finalMarker, options.SyncFilesystem); err != nil {
		return Identity{}, err
	}
	return finalIdentity, nil
}

func discardMigrationStaging(options IdentityOptions) error {
	stateRoot := filepath.Dir(options.ConfigDir)
	if err := requireRealDirectory(stateRoot); err != nil {
		return fmt.Errorf("validate identity state root: %w", err)
	}
	for _, name := range []string{"config.migrate.tmp", "data.migrate.tmp"} {
		if err := removeOwnedTemporary(stateRoot, filepath.Join(stateRoot, name)); err != nil {
			return err
		}
	}
	return nil
}

func copyIdentityFile(source, destination string, limit int64) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return errors.New("source is empty, oversized, symlinked, or not regular")
	}
	contents, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if int64(len(contents)) <= 0 || int64(len(contents)) > limit {
		return errors.New("source changed size while copying")
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func replaceGenerationMarker(configDir string, marker generationMarker, syncFilesystem SyncFilesystemFunc) error {
	markerPath := filepath.Join(configDir, GenerationMarkerName)
	temporaryPath := markerPath + ".tmp"
	if info, err := os.Lstat(temporaryPath); err == nil {
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return errors.New("syncthing identity: generation-marker temporary is a directory")
		}
		if err := os.Remove(temporaryPath); err != nil {
			return fmt.Errorf("discard generation-marker temporary: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := writeGenerationMarker(temporaryPath, marker); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, markerPath); err != nil {
		return fmt.Errorf("promote generation marker: %w", err)
	}
	if err := syncFilesystem(configDir); err != nil {
		return fmt.Errorf("flush migrated generation marker: %w", err)
	}
	return nil
}

func inspectGeneratedIdentity(ctx context.Context, binary, configDir, dataDir, upstreamVersion, guiSocket string) (Identity, generationMarker, error) {
	configPath := filepath.Join(configDir, "config.xml")
	if err := ValidateXML(configPath); err != nil {
		return Identity{}, generationMarker{}, fmt.Errorf("validate generated config: %w", err)
	}
	configuration, err := readConfig(configPath)
	if err != nil {
		return Identity{}, generationMarker{}, err
	}
	if configuration.Version <= 0 || !configuration.GUI.Enabled || strings.TrimSpace(configuration.GUI.APIKey) == "" {
		return Identity{}, generationMarker{}, errors.New("syncthing identity: generated config omits version, enabled GUI, or API key")
	}
	if configuration.GUI.UnixPermissions != "0600" || (guiSocket != "" && configuration.GUI.Address != guiSocket) {
		return Identity{}, generationMarker{}, errors.New("syncthing identity: generated config does not use the private GUI socket profile")
	}
	if configuration.Options.AutoUpgradeHours != 0 || configuration.Options.StartBrowser || configuration.Options.CrashReporting {
		return Identity{}, generationMarker{}, errors.New("syncthing identity: generated config enables an unmanaged background feature")
	}

	deviceID, err := readDeviceID(ctx, binary, configDir, dataDir)
	if err != nil {
		return Identity{}, generationMarker{}, err
	}
	foundSelf := false
	for _, device := range configuration.Devices {
		if device.ID == deviceID {
			foundSelf = true
			break
		}
	}
	if !foundSelf {
		return Identity{}, generationMarker{}, errors.New("syncthing identity: generated config does not contain its certificate-derived device id")
	}

	certificateSHA, err := hashIdentityFile(filepath.Join(configDir, "cert.pem"))
	if err != nil {
		return Identity{}, generationMarker{}, fmt.Errorf("validate generated certificate: %w", err)
	}
	privateKeySHA, err := hashIdentityFile(filepath.Join(configDir, "key.pem"))
	if err != nil {
		return Identity{}, generationMarker{}, fmt.Errorf("validate generated private key: %w", err)
	}
	marker := generationMarker{
		Schema: 1, UpstreamVersion: upstreamVersion, ConfigVersion: configuration.Version, DeviceID: deviceID,
		CertificateSHA: certificateSHA, PrivateKeySHA: privateKeySHA,
	}
	return Identity{DeviceID: deviceID, UpstreamVersion: upstreamVersion, ConfigVersion: configuration.Version}, marker, nil
}

func readConfig(path string) (parsedConfig, error) {
	var configuration parsedConfig
	info, err := os.Lstat(path)
	if err != nil {
		return configuration, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > MaxConfigBytes {
		return configuration, errors.New("syncthing identity: config is unsafe or oversized")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return configuration, err
	}
	if int64(len(contents)) > MaxConfigBytes {
		return configuration, errors.New("syncthing identity: config exceeds size limit")
	}
	if err := xml.Unmarshal(contents, &configuration); err != nil {
		return configuration, fmt.Errorf("parse generated config: %w", err)
	}
	return configuration, nil
}

func readDeviceID(ctx context.Context, binary, configDir, dataDir string) (string, error) {
	command := exec.CommandContext(ctx, binary, "--config="+configDir, "--data="+dataDir, "device-id")
	configureChild(command)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("derive device id with pinned Syncthing: %w", err)
	}
	deviceID := strings.TrimSpace(string(output))
	if !deviceIDPattern.MatchString(deviceID) {
		return "", errors.New("syncthing identity: pinned binary returned a malformed device id")
	}
	return deviceID, nil
}

func hashIdentityFile(path string) (string, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() || linkInfo.Size() <= 0 || linkInfo.Size() > maxIdentityFileBytes {
		return "", errors.New("identity file is empty, oversized, symlinked, or not regular")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxIdentityFileBytes {
		return "", errors.New("identity file is empty, oversized, or not regular")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeGenerationMarker(path string, marker generationMarker) error {
	contents, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create generation marker: %w", err)
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return fmt.Errorf("write generation marker: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("flush generation marker: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close generation marker: %w", err)
	}
	return nil
}

func readGenerationMarker(path string) (generationMarker, error) {
	var marker generationMarker
	info, err := os.Lstat(path)
	if err != nil {
		return marker, fmt.Errorf("read generation marker: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 4096 {
		return marker, errors.New("syncthing identity: generation marker is unsafe or oversized")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return marker, err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return marker, fmt.Errorf("parse generation marker: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return marker, errors.New("syncthing identity: generation marker contains trailing data")
	}
	if marker.Schema != 1 || marker.UpstreamVersion == "" || marker.ConfigVersion <= 0 || !deviceIDPattern.MatchString(marker.DeviceID) ||
		len(marker.CertificateSHA) != sha256.Size*2 || len(marker.PrivateKeySHA) != sha256.Size*2 {
		return marker, errors.New("syncthing identity: generation marker is incomplete")
	}
	return marker, nil
}

func removeOwnedTemporary(stateRoot, path string) error {
	relative, err := filepath.Rel(stateRoot, path)
	if err != nil || relative == "." || filepath.Dir(relative) != "." ||
		(relative != "config.generate.tmp" && relative != "data.generate.tmp" &&
			relative != "config.migrate.tmp" && relative != "data.migrate.tmp") {
		return fmt.Errorf("refuse unsafe generation cleanup path %q", path)
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return os.Remove(path)
	}
	if !info.IsDir() {
		return fmt.Errorf("generation temporary %s is not a directory", path)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove generation temporary %s: %w", path, err)
	}
	return nil
}

func (options IdentityOptions) validate() error {
	if options.Binary == "" || options.ConfigDir == "" || options.DataDir == "" || options.UpstreamVersion == "" || options.GUISocket == "" {
		return errors.New("syncthing identity: binary, config, data, upstream version, and GUI socket are required")
	}
	if !filepath.IsAbs(options.GUISocket) || filepath.Base(options.GUISocket) != "syncthing-gui.sock" {
		return errors.New("syncthing identity: GUI socket path is invalid")
	}
	if filepath.Dir(options.ConfigDir) != filepath.Dir(options.DataDir) ||
		filepath.Base(options.ConfigDir) != "config" || filepath.Base(options.DataDir) != "data" {
		return errors.New("syncthing identity: config and data must be canonical sibling directories")
	}
	info, err := os.Lstat(options.Binary)
	if err != nil {
		return fmt.Errorf("validate pinned Syncthing binary: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return errors.New("syncthing identity: pinned binary is not a real executable file")
	}
	return nil
}
