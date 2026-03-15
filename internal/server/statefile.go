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
		proc, err := os.FindProcess(sf.PID)
		if err != nil {
			os.Remove(path)
			continue
		}
		// Signal 0 checks process existence without sending
		// a signal.
		if err := proc.Signal(os.Signal(nil)); err != nil {
			os.Remove(path)
			continue
		}

		// Verify the port is actually listening.
		conn, err := net.DialTimeout(
			"tcp",
			fmt.Sprintf("127.0.0.1:%d", sf.Port),
			500*time.Millisecond,
		)
		if err != nil {
			os.Remove(path)
			continue
		}
		conn.Close()

		return &sf
	}

	return nil
}
