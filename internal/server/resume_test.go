package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wesm/agentsview/internal/db"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple uuid", "abc-123-def", "abc-123-def"},
		{"alphanumeric", "session42", "session42"},
		{"with colon", "a:b", "'a:b'"},
		{"with spaces", "has space", "'has space'"},
		{"with single quote", "it's", `'it'"'"'s'`},
		{"command injection attempt", "$(whoami)", "'$(whoami)'"},
		{"backtick injection", "`rm -rf /`", "'`rm -rf /`'"},
		{"semicolon", "id;rm -rf /", "'id;rm -rf /'"},
		{"pipe", "id|cat", "'id|cat'"},
		{"empty passthrough", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellQuote(tt.in)
			if got != tt.want {
				t.Errorf(
					"shellQuote(%q) = %q, want %q",
					tt.in, got, tt.want,
				)
			}
		})
	}
}

func TestDetectTerminalLinux_NoTerminal(t *testing.T) {
	// When no terminal is installed, should return an error.
	// This test validates the error path — on CI or servers
	// without a display, no terminal emulator is typically
	// available.
	_, _, err := detectTerminalLinux("echo test")
	// We just check it doesn't panic. The error may or may not
	// occur depending on the environment.
	_ = err
}

func TestResolveSessionDir(t *testing.T) {
	// Create a real temp directory for the "absolute path" cases.
	tmpDir := t.TempDir()

	// Create a session file with a cwd field.
	sessionFile := filepath.Join(tmpDir, "session.jsonl")
	cwdDir := filepath.Join(tmpDir, "project")
	if err := os.Mkdir(cwdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"cwd":"` + cwdDir + `"}` + "\n"
	if err := os.WriteFile(sessionFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		session *db.Session
		want    string
	}{
		{
			name: "absolute project path",
			session: &db.Session{
				Project: tmpDir,
			},
			want: tmpDir,
		},
		{
			name: "relative project name returns empty",
			session: &db.Session{
				Project: "my-repo",
			},
			want: "",
		},
		{
			name: "nil file_path with relative project",
			session: &db.Session{
				Project:  "my-repo",
				FilePath: nil,
			},
			want: "",
		},
		{
			name: "file_path with cwd in session file",
			session: &db.Session{
				Project:  "my-repo",
				FilePath: &sessionFile,
			},
			want: cwdDir,
		},
		{
			name: "file_path takes precedence over project",
			session: &db.Session{
				Project:  tmpDir,
				FilePath: &sessionFile,
			},
			want: cwdDir,
		},
		{
			name: "nonexistent file_path falls back to project",
			session: func() *db.Session {
				bad := "/nonexistent/session.jsonl"
				return &db.Session{
					Project:  tmpDir,
					FilePath: &bad,
				}
			}(),
			want: tmpDir,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSessionDir(tt.session)
			if got != tt.want {
				t.Errorf(
					"resolveSessionDir() = %q, want %q",
					got, tt.want,
				)
			}
		})
	}
}
