package gateway

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	maxTrustRecords = 32
	trustAbsolute   = 30 * 24 * time.Hour
	trustIdle       = 24 * time.Hour
	lastUsedFlush   = 5 * time.Minute
)

type trustRecord struct {
	Hash     string    `json:"hash"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used"`
}

type trustState struct {
	Schema  int           `json:"schema"`
	Records []trustRecord `json:"records"`
}

type trustStore struct {
	mu      sync.Mutex
	path    string
	now     func() time.Time
	random  io.Reader
	records []trustRecord
}

func newTrustStore(path string, now func() time.Time, random io.Reader) (*trustStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("trust store path must be absolute")
	}
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	store := &trustStore{path: path, now: now, random: random}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *trustStore) Count() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	changed := store.pruneLocked(store.now())
	if changed {
		_ = store.persistLocked()
	}
	return len(store.records)
}

func (store *trustStore) Issue() (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	store.pruneLocked(now)
	secret := make([]byte, 32)
	if _, err := io.ReadFull(store.random, secret); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256([]byte(token))
	store.records = append(store.records, trustRecord{Hash: hex.EncodeToString(digest[:]), Created: now, LastUsed: now})
	sort.Slice(store.records, func(left, right int) bool { return store.records[left].LastUsed.Before(store.records[right].LastUsed) })
	if len(store.records) > maxTrustRecords {
		store.records = append([]trustRecord(nil), store.records[len(store.records)-maxTrustRecords:]...)
	}
	if err := store.persistLocked(); err != nil {
		return "", err
	}
	return token, nil
}

func (store *trustStore) Authenticate(token string) bool {
	if token == "" {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(digest[:])
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	changed := store.pruneLocked(now)
	for index := range store.records {
		if store.records[index].Hash != hash {
			continue
		}
		if now.Sub(store.records[index].LastUsed) >= lastUsedFlush {
			store.records[index].LastUsed = now
			changed = true
		}
		if changed {
			_ = store.persistLocked()
		}
		return true
	}
	if changed {
		_ = store.persistLocked()
	}
	return false
}

func (store *trustStore) Revoke(token string) error {
	digest := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(digest[:])
	store.mu.Lock()
	defer store.mu.Unlock()
	kept := store.records[:0]
	for _, record := range store.records {
		if record.Hash != hash {
			kept = append(kept, record)
		}
	}
	store.records = kept
	return store.persistLocked()
}

func (store *trustStore) RevokeAll() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.records = nil
	return store.persistLocked()
}

func (store *trustStore) pruneLocked(now time.Time) bool {
	kept := store.records[:0]
	for _, record := range store.records {
		if now.Sub(record.Created) > trustAbsolute || now.Sub(record.LastUsed) > trustIdle || now.Before(record.Created) {
			continue
		}
		kept = append(kept, record)
	}
	changed := len(kept) != len(store.records)
	store.records = kept
	return changed
}

func (store *trustStore) load() error {
	info, err := os.Lstat(store.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64*1024 {
		return errors.New("trusted browser store is unsafe")
	}
	payload, err := os.ReadFile(store.path)
	if err != nil {
		return err
	}
	var state trustState
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || state.Schema != 1 || len(state.Records) > maxTrustRecords {
		return errors.New("trusted browser store is unsupported")
	}
	for _, record := range state.Records {
		decoded, err := hex.DecodeString(record.Hash)
		if err != nil || len(decoded) != sha256.Size || record.Created.IsZero() || record.LastUsed.IsZero() {
			return errors.New("trusted browser record is invalid")
		}
	}
	store.records = state.Records
	store.pruneLocked(store.now().UTC())
	return nil
}

func (store *trustStore) persistLocked() error {
	state := trustState{Schema: 1, Records: store.records}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}
	temporary := store.path + ".tmp"
	if info, err := os.Lstat(temporary); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("trusted browser temporary is unsafe")
		}
		if err := os.Remove(temporary); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
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
	return os.Rename(temporary, store.path)
}
