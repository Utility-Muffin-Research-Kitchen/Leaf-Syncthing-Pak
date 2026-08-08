package gateway

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrustStorePersistsOnlyHashesAndExpires(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "trusted-clients.json")
	store, err := newTrustStore(path, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{7}, 256)))
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.Issue()
	if err != nil || !store.Authenticate(token) || store.Count() != 1 {
		t.Fatalf("issue/authenticate = %q, %v, count=%d", token, err, store.Count())
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), token) {
		t.Fatal("raw trust token was persisted")
	}
	now = now.Add(trustIdle + time.Second)
	if store.Authenticate(token) || store.Count() != 0 {
		t.Fatal("idle-expired trust token remained valid")
	}
}

func TestTrustStoreCapsRecordsAndEnforcesAbsoluteExpiry(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "trusted-clients.json")
	entropy := make([]byte, 32*(maxTrustRecords+8))
	for record := 0; record < maxTrustRecords+8; record++ {
		for offset := 0; offset < 32; offset++ {
			entropy[record*32+offset] = byte(record + 1)
		}
	}
	store, err := newTrustStore(path, func() time.Time { return now },
		bytes.NewReader(entropy))
	if err != nil {
		t.Fatal(err)
	}
	tokens := make([]string, maxTrustRecords+8)
	for index := range tokens {
		tokens[index], err = store.Issue()
		if err != nil {
			t.Fatal(err)
		}
	}
	if store.Count() != maxTrustRecords || store.Authenticate(tokens[0]) ||
		!store.Authenticate(tokens[len(tokens)-1]) {
		t.Fatal("trust record cap did not retain only the newest records")
	}
	now = now.Add(trustAbsolute + time.Second)
	store.mu.Lock()
	for index := range store.records {
		store.records[index].LastUsed = now
	}
	store.mu.Unlock()
	if store.Authenticate(tokens[len(tokens)-1]) || store.Count() != 0 {
		t.Fatal("absolute-expired trust token remained valid")
	}
}
