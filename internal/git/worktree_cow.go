package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/safeurl"
	"github.com/kunchenguid/no-mistakes/internal/shellenv"
)

const maxCoWDiagnosticBytes = 8 << 10

// WorktreeAddCoW creates a detached gate worktree using verified files from an
// independent checkout. A bare gate has no checked-out donor of its own.
// A failed creation removes only the target path this call found absent before
// invoking cowtree, so an incomplete checkout cannot accumulate across runs.
func WorktreeAddCoW(ctx context.Context, gateDir, wtPath, sha, donorPath, cowtreePath string) (retErr error) {
	for name, path := range map[string]string{
		"gate": gateDir, "worktree": wtPath, "donor": donorPath, "cowtree": cowtreePath,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("CoW %s path must be absolute", name)
		}
	}
	if !isBareGitDir(gateDir) {
		return fmt.Errorf("CoW gate is not a bare repository")
	}
	if _, err := os.Lstat(wtPath); err == nil {
		return fmt.Errorf("CoW worktree path already exists: %s", wtPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check CoW worktree path: %w", err)
	}
	defer func() {
		if retErr == nil {
			return
		}
		if err := cleanupFailedCoWWorktree(gateDir, wtPath); err != nil {
			retErr = fmt.Errorf("%w; incomplete worktree cleanup at %s: %v", retErr, wtPath, err)
		}
	}()
	cmd := exec.CommandContext(ctx, cowtreePath, "add", "--detach", wtPath, sha)
	cmd.Dir = gateDir
	env := make([]string, 0, len(os.Environ())+3)
	for _, item := range nonInteractiveEnvForContext(ctx, gateDir) {
		if strings.HasPrefix(item, "GIT_DIR=") || strings.HasPrefix(item, "GIT_WORK_TREE=") ||
			strings.HasPrefix(item, "GIT_COMMON_DIR=") || strings.HasPrefix(item, "GIT_INDEX_FILE=") ||
			strings.HasPrefix(item, "GIT_PREFIX=") || strings.HasPrefix(item, "COWTREE_ADD_DONOR=") {
			continue
		}
		env = append(env, item)
	}
	env = append(env, "GIT_DIR="+gateDir, "COWTREE_ADD_DONOR="+donorPath)
	if original := os.Getenv("ATER_COW_ORIGINAL_PATH"); original != "" {
		env = append(env, "PATH="+original)
	}
	cmd.Env = env
	stdout := &cowDiagnosticBuffer{}
	stderr := &cowDiagnosticBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	shellenv.ConfigureShellCommand(cmd)
	err := shellenv.RunShellCommand(cmd)
	if err != nil {
		return fmt.Errorf("cowtree add: %w%s", err, cowDiagnostics(stdout, stderr))
	}
	receiptPath, err := Run(ctx, wtPath, "rev-parse", "--git-path", "cowtree-creation")
	if err != nil {
		return fmt.Errorf("locate CoW creation receipt: %w%s", err, cowDiagnostics(stdout, stderr))
	}
	if err := verifyCoWCreationReceipt(receiptPath, sha); err != nil {
		return fmt.Errorf("%w%s", err, cowDiagnostics(stdout, stderr))
	}
	return nil
}

func cleanupFailedCoWWorktree(gateDir, wtPath string) error {
	if _, err := os.Lstat(wtPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := WorktreeRemove(ctx, gateDir, wtPath); err != nil {
		// cowtree may have failed before Git registered the worktree. The path
		// was absent at entry and this call owns the target, so remove it here.
		if removeErr := os.RemoveAll(wtPath); removeErr != nil {
			return fmt.Errorf("git worktree remove: %v; remove partial directory: %w", err, removeErr)
		}
	}
	if _, err := os.Lstat(wtPath); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("partial worktree still exists")
	}
	return nil
}

func verifyCoWCreationReceipt(receiptPath, sha string) error {
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		return fmt.Errorf("read CoW creation receipt: %w", err)
	}
	var receipt struct {
		Version       int      `json:"version"`
		Operation     string   `json:"operation"`
		TargetCommit  string   `json:"target_commit"`
		SourceCommits []string `json:"source_commits"`
		ClonedFiles   uint64   `json:"cloned_files"`
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		return fmt.Errorf("parse CoW creation receipt: %w", err)
	}
	if receipt.Version != 1 || receipt.Operation != "create" || receipt.TargetCommit != sha ||
		len(receipt.SourceCommits) == 0 || receipt.ClonedFiles == 0 {
		return fmt.Errorf("CoW creation receipt does not prove cloning for %s", sha)
	}
	return nil
}

// Keep tool diagnostics useful without allowing a noisy subprocess to grow the
// daemon's memory or a run error without bound. A short write reports success
// to the child so diagnostics never change the creation result.
type cowDiagnosticBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *cowDiagnosticBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if remaining := maxCoWDiagnosticBytes - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			b.truncated = true
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	} else if written > 0 {
		b.truncated = true
	}
	return written, nil
}

func cowDiagnostics(stdout, stderr *cowDiagnosticBuffer) string {
	var parts []string
	for _, stream := range []struct {
		name string
		buf  *cowDiagnosticBuffer
	}{{"stderr", stderr}, {"stdout", stdout}} {
		message := strings.TrimSpace(stream.buf.buf.String())
		if message == "" && !stream.buf.truncated {
			continue
		}
		if stream.buf.truncated {
			message += " [truncated]"
		}
		parts = append(parts, stream.name+": "+safeurl.RedactText(message))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}
