package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatMtime_NonexistentFile(t *testing.T) {
	t.Parallel()
	got := statMtime(
		filepath.Join(t.TempDir(), "no-such-file"),
	)
	if got != 0 {
		t.Errorf("statMtime(nonexistent) = %d, want 0", got)
	}
}

func TestStatMtime_ExistingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(
		path, []byte("data"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	got := statMtime(path)
	if got == 0 {
		t.Error("statMtime(existing) = 0, want nonzero")
	}
}

func TestCheckDBForChanges_FileDisappears(t *testing.T) {
	t.Parallel()
	srv := testServer(t, 5*time.Second)

	path := filepath.Join(t.TempDir(), "gone.jsonl")
	var lastMtime int64 = 12345
	var mchanged time.Time
	var lastCount int
	var lastDBMtime int64

	changed := srv.checkDBForChanges(
		"test-session",
		&lastCount,
		&lastDBMtime,
		&path,
		&lastMtime,
		&mchanged,
	)
	if changed {
		t.Error("expected no change signal")
	}
	if path != "" {
		t.Errorf("sourcePath = %q, want empty", path)
	}
	if lastMtime != 0 {
		t.Errorf("lastMtime = %d, want 0", lastMtime)
	}
}
