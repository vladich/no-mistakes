package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/worktrees"
)

// CleanupRunWorktree is called only by explicit cleanup IPC. Terminal runs
// cannot be restarted in place by an IPC operation; a rerun gets a new ID.
func (m *RunManager) CleanupRunWorktree(ctx context.Context, runID string, discard bool) (*ipc.CleanupRunResult, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("cleanup requires an explicit run id")
	}
	m.cleanupMu.Lock()
	defer m.cleanupMu.Unlock()
	m.mu.Lock()
	busy := m.dones[runID] != nil || m.executors[runID] != nil
	m.mu.Unlock()
	run, err := m.db.GetRun(runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("unknown run %q", runID)
	}
	if busy || !run.Status.Terminal() {
		return nil, fmt.Errorf("run %s has not finished; cleanup refused", runID)
	}
	path := worktrees.RecordedDir(m.paths, run.WorktreePath(), run.RepoID, run.ID)
	result := &ipc.CleanupRunResult{RunID: runID, Path: path}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("run worktree is not a real directory: %s", path)
	}
	gate := m.paths.RepoDir(run.RepoID)
	if err := verifyCleanupWorktree(ctx, gate, path); err != nil {
		return nil, err
	}

	if !discard {
		status, err := git.Run(ctx, path, "status", "--porcelain", "--untracked-files=all", "--ignored=matching")
		if err != nil {
			return nil, fmt.Errorf("read worktree status: %w", err)
		}
		if strings.TrimSpace(status) != "" {
			return nil, fmt.Errorf("worktree has uncommitted files; preserve them or explicitly use --discard-uncommitted")
		}
	}
	m.sweepRunWorktreeProcesses(run.RepoID, run.ID, path)
	if err := git.WorktreeRemove(ctx, gate, path); err != nil {
		return nil, fmt.Errorf("remove run worktree: %w", err)
	}
	result.Removed = true
	return result, nil
}

func verifyCleanupWorktree(ctx context.Context, gate, path string) error {
	common, err := git.Run(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("verify worktree identity: %w", err)
	}
	top, err := git.Run(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil || worktrees.Canonical(strings.TrimSpace(top)) != worktrees.Canonical(path) ||
		worktrees.Canonical(strings.TrimSpace(common)) != worktrees.Canonical(gate) {
		return fmt.Errorf("run worktree identity changed; cleanup refused: %s", path)
	}
	return nil
}
