package parser

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExtractProjectFromCwd_Git(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string) string
		want  string
	}{
		{
			name: "GitRepoRoot",
			setup: func(t *testing.T, root string) string {
				repo := filepath.Join(root, "my-app")
				subdir := filepath.Join(repo, "internal", "sync")

				mustMkdirAll(t, filepath.Join(repo, ".git"))
				mustMkdirAll(t, subdir)
				return subdir
			},
			want: "my_app",
		},
		{
			name: "GitWorktree",
			setup: func(t *testing.T, root string) string {
				mainRepo := filepath.Join(root, "agentsview")
				worktree := filepath.Join(root, "agentsview-worktree-tool-calls")
				worktreeGitDir := filepath.Join(mainRepo, ".git", "worktrees", "feature")

				mustMkdirAll(t, filepath.Join(mainRepo, ".git"))
				mustMkdirAll(t, worktreeGitDir)
				mustMkdirAll(t, filepath.Join(worktree, "internal"))

				mustWriteFile(t, filepath.Join(worktree, ".git"),
					"gitdir: "+worktreeGitDir+"\n")
				// Matches git's linked-worktree layout.
				mustWriteFile(t, filepath.Join(worktreeGitDir, "commondir"), "../..\n")

				return filepath.Join(worktree, "internal")
			},
			want: "agentsview",
		},
		{
			name: "GitWorktreeFallbackWithoutCommondir",
			setup: func(t *testing.T, root string) string {
				mainRepo := filepath.Join(root, "my-repo")
				worktree := filepath.Join(root, "my-repo-experiment")
				worktreeGitDir := filepath.Join(mainRepo, ".git", "worktrees", "exp")

				mustMkdirAll(t, filepath.Join(mainRepo, ".git"))
				mustMkdirAll(t, worktreeGitDir)
				mustMkdirAll(t, worktree)

				mustWriteFile(t, filepath.Join(worktree, ".git"),
					"gitdir: "+worktreeGitDir+"\n")

				return worktree
			},
			want: "my_repo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			cwd := tt.setup(t, root)
			got := ExtractProjectFromCwd(cwd)
			if got != tt.want {
				t.Fatalf("ExtractProjectFromCwd(%q) = %q, want %q", cwd, got, tt.want)
			}
		})
	}
}

func TestExtractProjectFromCwd_DeletedNestedWorktree(t *testing.T) {
	// Simulates a nested worktree layout where the session's
	// worktree has been deleted but a sibling worktree still
	// exists and can reveal the true repo root.
	root := t.TempDir()

	mainRepo := filepath.Join(root, "my-project")
	mustMkdirAll(t, filepath.Join(mainRepo, ".git", "worktrees", "other-branch"))

	container := filepath.Join(root, "worktrees", "my-project")
	sibling := filepath.Join(container, "other-branch")
	mustMkdirAll(t, sibling)

	worktreeGitDir := filepath.Join(
		mainRepo, ".git", "worktrees", "other-branch",
	)
	mustWriteFile(t, filepath.Join(sibling, ".git"),
		"gitdir: "+worktreeGitDir+"\n")
	mustWriteFile(t, filepath.Join(worktreeGitDir, "commondir"),
		"../..\n")

	// The deleted worktree path — not created on disk.
	deleted := filepath.Join(container, "tauri-packaging")

	got := ExtractProjectFromCwd(deleted)
	if got != "my_project" {
		t.Fatalf(
			"ExtractProjectFromCwd(%q) = %q, want %q",
			deleted, got, "my_project",
		)
	}
}

func TestExtractProjectFromCwd_DeletedNestedWorktreeNoCommondir(
	t *testing.T,
) {
	// When commondir is missing, repoRootFromGitFile falls back
	// to the strings.Cut marker check. The gitdir must be
	// normalized so the separator-based marker matches.
	root := t.TempDir()

	mainRepo := filepath.Join(root, "my-project")
	worktreeGitDir := filepath.Join(
		mainRepo, ".git", "worktrees", "other-branch",
	)
	mustMkdirAll(t, filepath.Join(mainRepo, ".git"))
	mustMkdirAll(t, worktreeGitDir)
	// No commondir file written.

	container := filepath.Join(root, "worktrees", "my-project")
	sibling := filepath.Join(container, "other-branch")
	mustMkdirAll(t, sibling)

	mustWriteFile(t, filepath.Join(sibling, ".git"),
		"gitdir: "+worktreeGitDir+"\n")

	deleted := filepath.Join(container, "tauri-packaging")

	got := ExtractProjectFromCwd(deleted)
	if got != "my_project" {
		t.Fatalf(
			"ExtractProjectFromCwd(%q) = %q, want %q",
			deleted, got, "my_project",
		)
	}
}

func TestExtractProjectFromCwd_DeletedNestedWorktreeDeep(
	t *testing.T,
) {
	// When the deleted worktree path includes subdirectories
	// (e.g. .../tauri-packaging/cmd/server), the walk must
	// reach the first existing ancestor before sibling detection.
	root := t.TempDir()

	mainRepo := filepath.Join(root, "my-project")
	mustMkdirAll(t, filepath.Join(
		mainRepo, ".git", "worktrees", "other-branch",
	))

	container := filepath.Join(root, "worktrees", "my-project")
	sibling := filepath.Join(container, "other-branch")
	mustMkdirAll(t, sibling)

	worktreeGitDir := filepath.Join(
		mainRepo, ".git", "worktrees", "other-branch",
	)
	mustWriteFile(t, filepath.Join(sibling, ".git"),
		"gitdir: "+worktreeGitDir+"\n")
	mustWriteFile(t, filepath.Join(worktreeGitDir, "commondir"),
		"../..\n")

	// Nested path inside a deleted worktree — neither
	// tauri-packaging/ nor cmd/server/ exist on disk.
	deep := filepath.Join(
		container, "tauri-packaging", "cmd", "server",
	)

	got := ExtractProjectFromCwd(deep)
	if got != "my_project" {
		t.Fatalf(
			"ExtractProjectFromCwd(%q) = %q, want %q",
			deep, got, "my_project",
		)
	}
}

func TestExtractProjectFromCwd_SubmoduleSiblingIgnored(
	t *testing.T,
) {
	// A sibling directory with a submodule .git file (pointing
	// to .git/modules/) must not be mistaken for a linked
	// worktree. The function should return "" rather than the
	// submodule's repo root.
	root := t.TempDir()

	parentRepo := filepath.Join(root, "parent-repo")
	mustMkdirAll(t, filepath.Join(
		parentRepo, ".git", "modules", "sub-lib",
	))

	container := filepath.Join(root, "worktrees", "parent-repo")
	submod := filepath.Join(container, "sub-lib")
	mustMkdirAll(t, submod)

	// Submodule .git file: points to .git/modules/, not
	// .git/worktrees/.
	submodGitDir := filepath.Join(
		parentRepo, ".git", "modules", "sub-lib",
	)
	mustWriteFile(t, filepath.Join(submod, ".git"),
		"gitdir: "+submodGitDir+"\n")

	deleted := filepath.Join(container, "deleted-branch")

	got := ExtractProjectFromCwd(deleted)
	// No worktree sibling found, falls back to basename.
	if got != "deleted_branch" {
		t.Fatalf(
			"ExtractProjectFromCwd(%q) = %q, want %q",
			deleted, got, "deleted_branch",
		)
	}
}

func TestExtractProjectFromCwdWithBranch_NestedWorktree(
	t *testing.T,
) {
	// When a nested worktree is deleted and the branch name
	// matches the directory name, the sibling-based git root
	// detection should resolve the correct project name.
	root := t.TempDir()

	mainRepo := filepath.Join(root, "agentsview")
	mustMkdirAll(t, filepath.Join(
		mainRepo, ".git", "worktrees", "fix-worktrees",
	))

	container := filepath.Join(root, "worktrees", "agentsview")
	sibling := filepath.Join(container, "fix-worktrees")
	mustMkdirAll(t, sibling)

	worktreeGitDir := filepath.Join(
		mainRepo, ".git", "worktrees", "fix-worktrees",
	)
	mustWriteFile(t, filepath.Join(sibling, ".git"),
		"gitdir: "+worktreeGitDir+"\n")
	mustWriteFile(t, filepath.Join(worktreeGitDir, "commondir"),
		"../..\n")

	// Deleted worktree where branch name = directory name.
	deleted := filepath.Join(container, "tauri-packaging")

	got := ExtractProjectFromCwdWithBranch(
		deleted, "tauri-packaging",
	)
	if got != "agentsview" {
		t.Fatalf(
			"ExtractProjectFromCwdWithBranch(%q, %q) = %q, want %q",
			deleted, "tauri-packaging", got, "agentsview",
		)
	}
}

func TestExtractProjectFromCwdWithBranch(t *testing.T) {
	tests := []struct {
		name   string
		cwd    string
		branch string
		want   string
	}{
		{
			name:   "OfflineWorktreePath",
			cwd:    filepath.FromSlash("/Users/wesm/code/agentsview-worktree-tool-call-arguments"),
			branch: "worktree-tool-call-arguments",
			want:   "agentsview",
		},
		{
			name:   "BranchWithSlash",
			cwd:    filepath.FromSlash("/Users/wesm/code/agentsview-feature-worktree-support"),
			branch: "feature/worktree-support",
			want:   "agentsview",
		},
		{
			name:   "MismatchNoTrim",
			cwd:    filepath.FromSlash("/Users/wesm/code/agentsview-hotfix"),
			branch: "feature/other",
			want:   "agentsview_hotfix",
		},
		{
			name:   "DefaultBranchNoTrim",
			cwd:    filepath.FromSlash("/Users/wesm/code/project-main"),
			branch: "main",
			want:   "project_main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractProjectFromCwdWithBranch(tt.cwd, tt.branch)
			if got != tt.want {
				t.Fatalf("ExtractProjectFromCwdWithBranch(%q, %q) = %q, want %q", tt.cwd, tt.branch, got, tt.want)
			}
		})
	}
}

func TestForeignWindowsPathSkipsGitRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test is for non-Windows hosts only")
	}

	// On non-Windows, a Windows-style path like C:\repo\subdir
	// should NOT trigger findGitRepoRoot (which would walk the
	// process CWD). It should fall back to the basename.
	got := ExtractProjectFromCwdWithBranch(
		`C:\Users\dev\projects\my-app`, "",
	)
	if got != "my_app" {
		t.Errorf(
			"foreign Windows path: got %q, want %q",
			got, "my_app",
		)
	}
}

func TestNativeWindowsPathUsesGitRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("test is for Windows hosts only")
	}

	// On Windows, a drive-letter path inside a git repo should
	// still resolve to the repo root name, not the leaf dir.
	root := t.TempDir()
	repo := filepath.Join(root, "my-repo")
	subdir := filepath.Join(repo, "cmd", "server")
	mustMkdirAll(t, filepath.Join(repo, ".git"))
	mustMkdirAll(t, subdir)

	got := ExtractProjectFromCwd(subdir)
	if got != "my_repo" {
		t.Errorf(
			"native Windows git path: got %q, want %q",
			got, "my_repo",
		)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}
