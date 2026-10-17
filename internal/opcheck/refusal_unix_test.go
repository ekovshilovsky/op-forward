//go:build !windows

package opcheck

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ekovshilovsky/op-forward/internal/executor"
)

// A refused op must never be executed by the diagnostic: the fake would
// leave a marker file if any of the metadata or probe commands ran.
func TestCheckNeverExecutesARefusedOp(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := "#!/bin/sh\ntouch '" + marker + "'\necho 2.0.0\n"
	if err := os.WriteFile(filepath.Join(dir, "op"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	check(context.Background(), Options{Authenticated: true}, refuseWith(&executor.NotNativeExecutableError{Name: "op", Ext: ""}))

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("the refused op was executed (stat err: %v)", err)
	}
}
