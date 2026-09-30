package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVerifyCoWCreationReceiptRejectsUnprovenClones(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	valid := fmt.Sprintf(`{"version":1,"operation":"create","target_commit":%q,"source_commits":["donor"],"cloned_files":1}`, sha)
	for _, tc := range []struct {
		name, content, want string
		missing             bool
	}{
		{name: "missing", missing: true, want: "read CoW creation receipt"},
		{name: "malformed", content: "{", want: "parse CoW creation receipt"},
		{name: "zero clones", content: strings.Replace(valid, `"cloned_files":1`, `"cloned_files":0`, 1), want: "does not prove cloning"},
		{name: "no donor", content: strings.Replace(valid, `["donor"]`, `[]`, 1), want: "does not prove cloning"},
		{name: "wrong target", content: strings.Replace(valid, sha, "different", 1), want: "does not prove cloning"},
		{name: "wrong operation", content: strings.Replace(valid, `"create"`, `"compact"`, 1), want: "does not prove cloning"},
		{name: "valid", content: valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cowtree-creation")
			if !tc.missing {
				if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := verifyCoWCreationReceipt(path, sha)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid receipt rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("receipt error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestWorktreeAddCoWFailureRemovesOnlyNewTargetAndReportsDiagnostics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake cowtree uses a POSIX shell; receipt validation above runs on every platform")
	}
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "gate.git")
	if err := InitBare(ctx, bare); err != nil {
		t.Fatal(err)
	}
	donor := t.TempDir()
	tool := filepath.Join(t.TempDir(), "fake-cowtree")
	script := "#!/bin/sh\nmkdir -p \"$3\"\nprintf 'partial' > \"$3/partial.txt\"\nprintf 'setup begun\\n'\nprintf 'donor unavailable at https://user:password@example.com/repo\\n' >&2\nexit 7\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "worktree")
	err := WorktreeAddCoW(ctx, bare, target, strings.Repeat("a", 40), donor, tool)
	if err == nil || !strings.Contains(err.Error(), "donor unavailable") || !strings.Contains(err.Error(), "setup begun") {
		t.Fatalf("missing tool diagnostics: %v", err)
	}
	if strings.Contains(err.Error(), "password") {
		t.Fatalf("tool diagnostics leaked URL credentials: %v", err)
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		t.Fatalf("failed creation left a partial worktree: %v", statErr)
	}

	preexisting := filepath.Join(t.TempDir(), "preexisting")
	if err := os.Mkdir(preexisting, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(preexisting, "owner.txt")
	if err := os.WriteFile(marker, []byte("other owner"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WorktreeAddCoW(ctx, bare, preexisting, strings.Repeat("a", 40), donor, tool); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("preexisting target was not refused: %v", err)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "other owner" {
		t.Fatalf("preexisting target changed: %q, %v", content, err)
	}
}

func TestWorktreeAddCoWRejectsBadReceiptInRoutineTests(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake cowtree uses a POSIX shell; receipt validation above runs on every platform")
	}
	ctx := context.Background()
	donor := initTestRepo(t)
	bare := filepath.Join(t.TempDir(), "gate.git")
	if err := InitBare(ctx, bare); err != nil {
		t.Fatal(err)
	}
	run(t, donor, "git", "push", bare, "HEAD:refs/heads/main")
	sha := run(t, donor, "git", "rev-parse", "HEAD")
	tool := filepath.Join(t.TempDir(), "fake-cowtree")
	script := `#!/bin/sh
set -eu
gate="$GIT_DIR"
git --git-dir="$gate" worktree add --detach "$3" "$4" >/dev/null
unset GIT_DIR
receipt=$(git -C "$3" rev-parse --git-path cowtree-creation)
case "$COW_TEST_RECEIPT" in
  missing) ;;
  malformed) printf '{' > "$receipt" ;;
  zero) printf '{"version":1,"operation":"create","target_commit":"%s","source_commits":["donor"],"cloned_files":0}' "$4" > "$receipt" ;;
esac
`
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing", "malformed", "zero"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("COW_TEST_RECEIPT", kind)
			target := filepath.Join(t.TempDir(), "worktree")
			if err := WorktreeAddCoW(ctx, bare, target, sha, donor, tool); err == nil {
				t.Fatal("unproven CoW worktree was accepted")
			}
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("rejected CoW worktree was not removed: %v", err)
			}
			if registered := run(t, donor, "git", "worktree", "list", "--porcelain"); strings.Contains(registered, target) {
				t.Fatalf("rejected CoW worktree remains registered: %s", registered)
			}
		})
	}
}

func TestCoWDiagnosticsAreBounded(t *testing.T) {
	stdout := &cowDiagnosticBuffer{}
	stderr := &cowDiagnosticBuffer{}
	if n, err := stderr.Write([]byte(strings.Repeat("x", maxCoWDiagnosticBytes*4))); n != maxCoWDiagnosticBytes*4 || err != nil {
		t.Fatalf("capture write = (%d, %v)", n, err)
	}
	message := cowDiagnostics(stdout, stderr)
	if !strings.Contains(message, "[truncated]") || len(message) > maxCoWDiagnosticBytes+80 {
		t.Fatalf("diagnostic was not bounded: length=%d", len(message))
	}
}
