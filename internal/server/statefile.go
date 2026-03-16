// ABOUTME: Manages server state files so CLI commands can detect
// ABOUTME: a running agentsview server instance.
package server

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StateFile records a running server instance.
type StateFile struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	Host      string `json:"host"`
	Version   string `json:"version"`
	StartedAt string `json:"started_at"`
}

// stateFileName returns the filename for a given port.
func stateFileName(port int) string {
	return fmt.Sprintf("server.%d.json", port)
}

// WriteStateFile writes a state file to dataDir for the
// running server. Returns the path written.
func WriteStateFile(
	dataDir string, host string, port int, version string,
) (string, error) {
	sf := StateFile{
		PID:       os.Getpid(),
		Port:      port,
		Host:      host,
		Version:   version,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(sf)
	if err != nil {
		return "", fmt.Errorf("marshaling state file: %w", err)
	}
	path := filepath.Join(dataDir, stateFileName(port))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("writing state file: %w", err)
	}
	return path, nil
}

// RemoveStateFile removes the state file for the given port.
func RemoveStateFile(dataDir string, port int) {
	os.Remove(filepath.Join(dataDir, stateFileName(port)))
}

// FindRunningServer scans dataDir for server state files and
// returns the first one whose process is still alive and whose
// port is accepting connections. Stale state files are cleaned
// up automatically.
func FindRunningServer(dataDir string) *StateFile {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil
	}

	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "server.") ||
			!strings.HasSuffix(name, ".json") {
			continue
		}

		path := filepath.Join(dataDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var sf StateFile
		if err := json.Unmarshal(data, &sf); err != nil {
			continue
		}

		// Check if the process is still running.
		if !processAlive(sf.PID) {
			os.Remove(path)
			continue
		}

		// Verify the port is actually listening. If the
		// process is alive but the dial fails (transient
		// timeout, GC pause, full backlog), keep the state
		// file — only a dead PID justifies removal.
		probeHost := probeHostForDial(sf.Host)
		conn, err := net.DialTimeout(
			"tcp",
			net.JoinHostPort(probeHost, fmt.Sprint(sf.Port)),
			500*time.Millisecond,
		)
		if err != nil {
			continue
		}
		conn.Close()

		return &sf
	}

	return nil
}

// hasLiveStateFile reports whether any server state file in
// dataDir has a live PID, regardless of port connectivity.
// Unlike FindRunningServer, this returns true even during
// transient TCP probe failures.
//
// To mitigate PID reuse after a reboot (where the OS can
// assign the same PID to an unrelated process), state files
// whose StartedAt predates the system boot time are treated
// as stale and removed. Between reboots, PID reuse requires
// cycling through the full PID space which is large on modern
// systems.
func hasLiveStateFile(dataDir string) bool {
	bootTime, _ := systemBootTime()

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "server.") ||
			!strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dataDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var sf StateFile
		if err := json.Unmarshal(data, &sf); err != nil {
			continue
		}
		if !processAlive(sf.PID) {
			continue
		}
		// If the state file predates the last boot, the PID
		// belongs to a different process. Clean up.
		if !bootTime.IsZero() && sf.StartedAt != "" {
			started, err := time.Parse(
				time.RFC3339, sf.StartedAt,
			)
			if err == nil && started.Before(bootTime) {
				os.Remove(path)
				continue
			}
		}
		return true
	}
	return false
}

const startupLockName = "server.starting"

// WriteStartupLock creates a lock file indicating a server is
// starting up (syncing, binding port). Written via a temp file
// and atomic rename to prevent concurrent readers from seeing
// a partial/empty file.
func WriteStartupLock(dataDir string) {
	target := filepath.Join(dataDir, startupLockName)
	tmp := target + ".tmp"
	data := fmt.Appendf(nil, "%d", os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		// Best-effort: fall back to direct write.
		_ = os.WriteFile(target, data, 0o644)
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.WriteFile(target, data, 0o644)
		os.Remove(tmp)
	}
}

// RemoveStartupLock removes the startup lock file.
func RemoveStartupLock(dataDir string) {
	os.Remove(filepath.Join(dataDir, startupLockName))
}

// isServerStarting reports whether a server is currently
// starting up. Returns true only if the lock file exists and
// the recorded PID is still alive.
func isServerStarting(dataDir string) bool {
	path := filepath.Join(dataDir, startupLockName)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		// Don't delete on parse failure — could be a
		// partial write from a concurrent WriteStartupLock.
		return false
	}
	if !processAlive(pid) {
		os.Remove(path)
		return false
	}
	return true
}

// IsStartupLocked reports whether the startup lock file exists
// with a live PID. Callers use this to distinguish "server is
// starting up" from "server is running but TCP probe failed".
func IsStartupLocked(dataDir string) bool {
	return isServerStarting(dataDir)
}

// IsServerActive reports whether a server process is managing
// the database in dataDir. Returns true if:
//   - a state file with a live PID exists (even if the port
//     probe fails due to a transient issue), or
//   - a startup lock with a live PID exists (server is still
//     syncing / binding its port).
//
// This is the check CLI commands should use to decide whether
// to skip on-demand sync.
func IsServerActive(dataDir string) bool {
	return hasLiveStateFile(dataDir) || isServerStarting(dataDir)
}

// WaitForStartup polls until the startup lock clears or a
// running server is detected, up to the given timeout.
// Returns true if a server became ready, false on timeout.
func WaitForStartup(dataDir string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if FindRunningServer(dataDir) != nil {
			return true
		}
		if !isServerStarting(dataDir) {
			// Lock gone but no running server — startup
			// may have failed. Caller should try on-demand
			// sync.
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// probeHostForDial converts a bind-all address to a loopback
// address suitable for a TCP readiness probe, matching the
// normalization used by the server startup checks.
func probeHostForDial(host string) string {
	switch host {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::":
		return "::1"
	default:
		return host
	}
}
