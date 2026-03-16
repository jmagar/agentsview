package server

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteAndRemoveStateFile(t *testing.T) {
	dir := t.TempDir()

	path, err := WriteStateFile(dir, "127.0.0.1", 8080, "1.0.0")
	if err != nil {
		t.Fatalf("WriteStateFile: %v", err)
	}

	want := filepath.Join(dir, "server.8080.json")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading state file: %v", err)
	}

	var sf StateFile
	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sf.Port != 8080 {
		t.Errorf("port = %d, want 8080", sf.Port)
	}
	if sf.Host != "127.0.0.1" {
		t.Errorf("host = %q, want 127.0.0.1", sf.Host)
	}
	if sf.Version != "1.0.0" {
		t.Errorf("version = %q, want 1.0.0", sf.Version)
	}
	if sf.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", sf.PID, os.Getpid())
	}
	if sf.StartedAt == "" {
		t.Error("started_at is empty")
	}

	RemoveStateFile(dir, 8080)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("state file not removed")
	}
}

func TestFindRunningServer_NoFiles(t *testing.T) {
	dir := t.TempDir()
	if sf := FindRunningServer(dir); sf != nil {
		t.Errorf("expected nil, got %+v", sf)
	}
}

func TestFindRunningServer_StaleFile(t *testing.T) {
	dir := t.TempDir()

	// Write a state file with a PID that doesn't exist.
	sf := StateFile{
		PID:       999999999,
		Port:      9999,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: "2025-01-01T00:00:00Z",
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(dir, "server.9999.json")
	os.WriteFile(path, data, 0o644)

	result := FindRunningServer(dir)
	if result != nil {
		t.Errorf("expected nil for stale PID, got %+v", result)
	}

	// Stale file should be cleaned up.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("stale state file not cleaned up")
	}
}

func TestFindRunningServer_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.8080.json")
	os.WriteFile(path, []byte("not json"), 0o644)

	result := FindRunningServer(dir)
	if result != nil {
		t.Errorf("expected nil for invalid JSON, got %+v", result)
	}
}

func TestFindRunningServer_IgnoresNonStateFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(
		filepath.Join(dir, "config.json"),
		[]byte("{}"), 0o644,
	)
	os.WriteFile(
		filepath.Join(dir, "server.txt"),
		[]byte("nope"), 0o644,
	)

	result := FindRunningServer(dir)
	if result != nil {
		t.Errorf("expected nil, got %+v", result)
	}
}

func TestFindRunningServer_LiveProcess(t *testing.T) {
	dir := t.TempDir()

	// Start a real TCP listener so the port probe succeeds.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	sf := StateFile{
		PID:       os.Getpid(),
		Port:      port,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: "2025-01-01T00:00:00Z",
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(
		dir, fmt.Sprintf("server.%d.json", port),
	)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	result := FindRunningServer(dir)
	if result == nil {
		t.Fatal("expected running server, got nil")
	}
	if result.Port != port {
		t.Errorf("port = %d, want %d", result.Port, port)
	}
	if result.PID != os.Getpid() {
		t.Errorf(
			"pid = %d, want %d", result.PID, os.Getpid(),
		)
	}
}

func TestFindRunningServer_BindAll(t *testing.T) {
	dir := t.TempDir()

	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	sf := StateFile{
		PID:       os.Getpid(),
		Port:      port,
		Host:      "0.0.0.0",
		Version:   "1.0.0",
		StartedAt: "2025-01-01T00:00:00Z",
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(
		dir, fmt.Sprintf("server.%d.json", port),
	)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	result := FindRunningServer(dir)
	if result == nil {
		t.Fatal(
			"expected running server for 0.0.0.0 host, got nil",
		)
	}
	if result.Port != port {
		t.Errorf("port = %d, want %d", result.Port, port)
	}
}

// recentStartedAt returns a StartedAt timestamp from 1 hour
// ago. This is always after the last boot, so the boot-time
// check in hasLiveStateFile passes.
func recentStartedAt() string {
	return time.Now().Add(-1 * time.Hour).UTC().
		Format(time.RFC3339)
}

// TestIsServerActive_LivePIDNoPort verifies that IsServerActive
// returns true when a state file has a live PID but no listening
// port (e.g., transient TCP probe failure or server under load).
func TestIsServerActive_LivePIDNoPort(t *testing.T) {
	dir := t.TempDir()

	sf := StateFile{
		PID:       os.Getpid(),
		Port:      59999,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: recentStartedAt(),
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(dir, "server.59999.json")
	os.WriteFile(path, data, 0o644)

	// FindRunningServer should return nil (no TCP).
	if FindRunningServer(dir) != nil {
		t.Error("expected FindRunningServer nil (no listener)")
	}

	// But IsServerActive should return true (live PID).
	if !IsServerActive(dir) {
		t.Error("expected IsServerActive true for live PID")
	}

	// State file should NOT be deleted.
	if _, err := os.Stat(path); err != nil {
		t.Error("state file was deleted despite live PID")
	}
}

// TestIsServerActive_LivePIDNoPort_NoStartupLock verifies
// the exact scenario where a server is running but the TCP
// probe is transiently failing: IsServerActive is true,
// FindRunningServer is nil, but IsStartupLocked is false.
// token-use should NOT enter the wait path or fall back to
// on-demand sync in this case.
func TestIsServerActive_LivePIDNoPort_NoStartupLock(
	t *testing.T,
) {
	dir := t.TempDir()

	sf := StateFile{
		PID:       os.Getpid(),
		Port:      59998,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: recentStartedAt(),
	}
	data, _ := json.Marshal(sf)
	os.WriteFile(
		filepath.Join(dir, "server.59998.json"), data, 0o644,
	)

	if FindRunningServer(dir) != nil {
		t.Error("expected FindRunningServer nil")
	}
	if !IsServerActive(dir) {
		t.Error("expected IsServerActive true")
	}
	if IsStartupLocked(dir) {
		t.Error("expected IsStartupLocked false")
	}
}

// TestIsServerActive_LongRunningServer verifies that a
// server running for weeks is still detected as active even
// when the TCP probe transiently fails. The state file must
// not be deleted regardless of age, as long as it post-dates
// the last boot.
func TestIsServerActive_LongRunningServer(t *testing.T) {
	dir := t.TempDir()

	// Use a StartedAt that's 30 days ago but after boot.
	// (If the machine has been up less than 30 days, use a
	// time just after boot.)
	bootTime, btErr := systemBootTime()
	startedAt := time.Now().Add(-30 * 24 * time.Hour)
	if btErr == nil && startedAt.Before(bootTime) {
		startedAt = bootTime.Add(time.Second)
	}

	sf := StateFile{
		PID:       os.Getpid(),
		Port:      59997,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: startedAt.UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(dir, "server.59997.json")
	os.WriteFile(path, data, 0o644)

	if !IsServerActive(dir) {
		t.Error("expected IsServerActive true for old but live PID")
	}

	// State file must NOT be deleted.
	if _, err := os.Stat(path); err != nil {
		t.Error("state file was deleted for long-running server")
	}
}

// TestIsServerActive_PreBootStateFile verifies that a state
// file from before the last system boot is treated as stale
// even if the PID is alive (PID reuse after reboot).
func TestIsServerActive_PreBootStateFile(t *testing.T) {
	dir := t.TempDir()

	bootTime, err := systemBootTime()
	if err != nil {
		t.Skipf("boot time not available: %v", err)
	}

	// State file from well before boot — simulates a crash
	// followed by a reboot where the PID was reused.
	preBootTime := bootTime.Add(-24 * time.Hour)
	sf := StateFile{
		PID:       os.Getpid(),
		Port:      59996,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: preBootTime.UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(dir, "server.59996.json")
	os.WriteFile(path, data, 0o644)

	if IsServerActive(dir) {
		t.Error(
			"expected false for pre-boot state file " +
				"(PID reuse after reboot)",
		)
	}

	// Stale file should be cleaned up.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("pre-boot state file was not cleaned up")
	}
}

// TestIsServerActive_StartupLock verifies that IsServerActive
// returns true when only the startup lock exists.
func TestIsServerActive_StartupLock(t *testing.T) {
	dir := t.TempDir()

	if IsServerActive(dir) {
		t.Fatal("expected false with no files")
	}

	WriteStartupLock(dir)
	if !IsServerActive(dir) {
		t.Fatal("expected true with startup lock")
	}

	RemoveStartupLock(dir)
	if IsServerActive(dir) {
		t.Fatal("expected false after lock removed")
	}
}

func TestStartupLock_OwnProcess(t *testing.T) {
	dir := t.TempDir()

	if isServerStarting(dir) {
		t.Fatal("expected false before lock written")
	}

	WriteStartupLock(dir)
	if !isServerStarting(dir) {
		t.Fatal("expected true after lock written")
	}

	RemoveStartupLock(dir)
	if isServerStarting(dir) {
		t.Fatal("expected false after lock removed")
	}
}

func TestStartupLock_StalePID(t *testing.T) {
	dir := t.TempDir()

	// Write a lock file with a PID that doesn't exist.
	path := filepath.Join(dir, startupLockName)
	os.WriteFile(path, []byte("999999999"), 0o644)

	if isServerStarting(dir) {
		t.Fatal("expected false for stale PID")
	}

	// Stale lock should be cleaned up.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("stale startup lock not cleaned up")
	}
}

// TestStartupLock_MalformedContent verifies that a malformed
// lock file (e.g., partial write) is not deleted, since it
// could be a concurrent WriteStartupLock in progress.
func TestStartupLock_MalformedContent(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, startupLockName)
	os.WriteFile(path, []byte("not-a-pid"), 0o644)

	if isServerStarting(dir) {
		t.Fatal("expected false for malformed content")
	}

	// File should NOT be deleted.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("malformed lock file was deleted")
	}
}

// TestStartupLock_AtomicWrite verifies the lock file is written
// with content intact (no empty/partial file observable).
func TestStartupLock_AtomicWrite(t *testing.T) {
	dir := t.TempDir()

	WriteStartupLock(dir)

	path := filepath.Join(dir, startupLockName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading lock: %v", err)
	}

	want := fmt.Sprintf("%d", os.Getpid())
	if string(data) != want {
		t.Errorf("lock content = %q, want %q", data, want)
	}

	// No temp file should remain.
	tmpPath := path + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temp file was not cleaned up")
	}
}

func TestWaitForStartup_AlreadyRunning(t *testing.T) {
	dir := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	WriteStateFile(dir, "127.0.0.1", port, "1.0.0")

	// Should return immediately since server is running.
	if !WaitForStartup(dir, 100*millisecondsForTest) {
		t.Error("expected true, server is running")
	}
}

func TestWaitForStartup_LockClearsNoServer(t *testing.T) {
	dir := t.TempDir()

	// No lock, no server — should return false immediately.
	if WaitForStartup(dir, 100*millisecondsForTest) {
		t.Error(
			"expected false, no lock and no server",
		)
	}
}

// millisecondsForTest is a scaling factor for test timeouts.
const millisecondsForTest = 1_000_000 // 1ms in ns

func TestProbeHostForDial(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"", "127.0.0.1"},
		{"0.0.0.0", "127.0.0.1"},
		{"::", "::1"},
		{"127.0.0.1", "127.0.0.1"},
		{"192.168.1.100", "192.168.1.100"},
	}
	for _, tt := range tests {
		got := probeHostForDial(tt.host)
		if got != tt.want {
			t.Errorf(
				"probeHostForDial(%q) = %q, want %q",
				tt.host, got, tt.want,
			)
		}
	}
}

func TestStateFileName(t *testing.T) {
	tests := []struct {
		port int
		want string
	}{
		{8080, "server.8080.json"},
		{3000, "server.3000.json"},
		{443, "server.443.json"},
	}
	for _, tt := range tests {
		got := stateFileName(tt.port)
		if got != tt.want {
			t.Errorf(
				"stateFileName(%d) = %q, want %q",
				tt.port, got, tt.want,
			)
		}
	}
}
