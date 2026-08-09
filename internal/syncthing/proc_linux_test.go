//go:build linux

package syncthing

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcNetHasListener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tcp")
	contents := "  sl  local_address rem_address st\n" +
		"   0: 0100007F:20C0 00000000:0000 0A 00000000:00000000\n" +
		"   1: 0100007F:20C1 00000000:0000 01 00000000:00000000\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	bound, err := procNetHasListener(path, 8384)
	if err != nil || !bound {
		t.Fatalf("listener = %v, %v", bound, err)
	}
	bound, err = procNetHasListener(path, 8385)
	if err != nil || bound {
		t.Fatalf("non-listener = %v, %v", bound, err)
	}
}
