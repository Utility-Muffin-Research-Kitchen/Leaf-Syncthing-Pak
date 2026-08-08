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
