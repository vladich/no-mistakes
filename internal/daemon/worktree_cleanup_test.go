package daemon

import (
	"context"
	"fmt"
	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/pipeline/steps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestCallerCleanupPreservesUncommittedWorkUntilExplicitRequest(t *testing.T) {
	for _, status := range []types.RunStatus{types.RunCompleted, types.RunFailed, types.RunCancelled} {
		t.Run(string(status), func(t *testing.T) {
			p := paths.WithRoot(t.TempDir())
			if err := p.EnsureDirs(); err != nil {
				t.Fatal(err)
			}
			d, err := db.Open(p.DB())
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			repo, head := setupTestGitRepo(t, p, d, "retention")
			run, err := d.InsertRun(repo.ID, "feature", head, head)
			if err != nil {
				t.Fatal(err)
			}
			// Recorded external placement must survive a subsequent config change.
			wt := filepath.Join(t.TempDir(), "scratch")
			if err := git.WorktreeAdd(context.Background(), p.RepoDir(repo.ID), wt, head); err != nil {
				t.Fatal(err)
			}
			if err := d.SetRunWorktreeDir(run.ID, wt); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wt, "test.txt"), []byte("staged repair\n"), 0600); err != nil {
				t.Fatal(err)
			}
			gitCmd(t, wt, "add", "test.txt")
			files := map[string]string{"test.txt": "unfinished repair\n", "untracked.txt": "recovery evidence\n"}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(wt, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := gitOutput(t, wt, "status", "--porcelain")
			if err := d.UpdateRunStatus(run.ID, status); err != nil {
				t.Fatal(err)
			}
			mgr := NewRunManager(d, p, nil)
			mgr.retainRunWorktree(repo.ID, run.ID, wt, "test_timeout_or_completion")
			cleanupOrphanWorktrees(d, p, leftoverRecordedRunWorktrees(d, p))
			mgr = NewRunManager(d, p, nil)
			if got := gitOutput(t, wt, "status", "--porcelain"); got != before {
				t.Fatalf("index/status changed: %q != %q", got, before)
			}
			if got := gitOutput(t, wt, "show", ":test.txt"); got != "staged repair" {
				t.Fatalf("staged repair lost: %q", got)
			}
			for name, want := range files {
				got, err := os.ReadFile(filepath.Join(wt, name))
				if err != nil || string(got) != want {
					t.Fatalf("%s lost: %q, %v", name, got, err)
				}
			}
			if _, err := mgr.CleanupRunWorktree(context.Background(), run.ID, false); err == nil || !strings.Contains(err.Error(), "uncommitted") {
				t.Fatalf("dirty cleanup = %v", err)
			}
			result, err := mgr.CleanupRunWorktree(context.Background(), run.ID, true)
			if err != nil || !result.Removed {
				t.Fatalf("explicit discard = %+v, %v", result, err)
			}
			if _, err := os.Stat(wt); !os.IsNotExist(err) {
				t.Fatalf("explicit removal failed: %v", err)
			}
			result, err = mgr.CleanupRunWorktree(context.Background(), run.ID, true)
			if err != nil || result.Removed {
				t.Fatalf("repeat cleanup = %+v, %v", result, err)
			}
		})
	}
}

func TestCallerCleanupRefusesActiveUnknownAndReplacedWorktrees(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo, head := setupTestGitRepo(t, p, d, "guard")
	run, err := d.InsertRun(repo.ID, "feature", head, head)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewRunManager(d, p, nil)
	for _, id := range []string{"", "unknown", run.ID} {
		if _, err := mgr.CleanupRunWorktree(context.Background(), id, true); err == nil {
			t.Fatalf("unsafe cleanup accepted: %q", id)
		}
	}
	if err := d.UpdateRunStatus(run.ID, types.RunCompleted); err != nil {
		t.Fatal(err)
	}
	// An unrelated checkout at the recorded coordinate must never be deleted.
	if err := d.SetRunWorktreeDir(run.ID, repo.WorkingPath); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CleanupRunWorktree(context.Background(), run.ID, true); err == nil {
		t.Fatal("removed unrelated checkout")
	}
	if _, err := os.Stat(filepath.Join(repo.WorkingPath, "test.txt")); err != nil {
		t.Fatal(err)
	}
}

// Exercise the actual Review repair deadline and daemon lifecycle together.
// The fake agent writes all three kinds of uncommitted state, then waits for
// cancellation without committing, exactly as in the reported data loss.
type timedRepairAgent struct{}

func (timedRepairAgent) Name() string { return "retention-fixture" }
func (timedRepairAgent) Close() error { return nil }
func (timedRepairAgent) Run(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
	if opts.Purpose != "review-fix" {
		return nil, fmt.Errorf("unexpected purpose: %s", opts.Purpose)
	}
	if err := os.WriteFile(filepath.Join(opts.CWD, "test.txt"), []byte("staged repair\n"), 0600); err != nil {
		return nil, err
	}
	if _, err := git.Run(ctx, opts.CWD, "add", "test.txt"); err != nil {
		return nil, err
	}
	for name, data := range map[string]string{"test.txt": "unfinished repair\n", "untracked.txt": "recovery evidence\n"} {
		if err := os.WriteFile(filepath.Join(opts.CWD, name), []byte(data), 0600); err != nil {
			return nil, err
		}
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type timedRepairStep struct{}

func (timedRepairStep) Name() types.StepName { return types.StepReview }
func (timedRepairStep) Execute(sctx *pipeline.StepContext) (*pipeline.StepOutcome, error) {
	sctx.Agent = timedRepairAgent{}
	sctx.Config.ReviewAgentTimeout = time.Second
	sctx.Fixing = true
	sctx.PreviousFindings = `{"findings":[{"id":"repair","action":"auto-fix","description":"Repair the tracked file"}]}`
	return (&steps.ReviewStep{}).Execute(sctx)
}
func TestCallerCleanupAfterActualRepairTimeoutAndDaemonRestart(t *testing.T) {
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step { return []pipeline.Step{timedRepairStep{}} })
	repo, head := setupTestGitRepo(t, p, d, "timed-repair")
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var pushed ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{Gate: p.RepoDir(repo.ID), Ref: "refs/heads/main", Old: strings.Repeat("0", 40), New: head}, &pushed); err != nil {
		t.Fatal(err)
	}
	run := waitForRunTerminalState(t, d, pushed.RunID)
	if run.Status != types.RunFailed || run.Error == nil || !strings.Contains(*run.Error, "absolute wall-clock limit") {
		t.Fatalf("repair did not time out: %+v", run)
	}
	wt := p.WorktreeDir(repo.ID, run.ID)
	if err := client.Call(ipc.MethodShutdown, nil, nil); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(p.Socket()); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon failed to stop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for {
		lock, err := acquireSingletonLock(p)
		if err == nil {
			lock.Release()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not release ownership: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	runTestDaemon(t, p, d, nil, 3*time.Second)
	for name, want := range map[string]string{"test.txt": "unfinished repair\n", "untracked.txt": "recovery evidence\n"} {
		got, err := os.ReadFile(filepath.Join(wt, name))
		if err != nil || string(got) != want {
			t.Fatalf("repair lost after restart: %s: %q, %v", name, got, err)
		}
	}
	if got := gitOutput(t, wt, "show", ":test.txt"); got != "staged repair" {
		t.Fatalf("staged repair lost: %q", got)
	}
	resumed, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if err := resumed.Call(ipc.MethodCleanupRun, &ipc.CleanupRunParams{RunID: run.ID}, nil); err == nil {
		t.Fatal("dirty scratch was discarded without explicit request")
	}
	var removed ipc.CleanupRunResult
	if err := resumed.Call(ipc.MethodCleanupRun, &ipc.CleanupRunParams{RunID: run.ID, DiscardUncommitted: true}, &removed); err != nil || !removed.Removed {
		t.Fatalf("explicit cleanup: %+v, %v", removed, err)
	}
}
