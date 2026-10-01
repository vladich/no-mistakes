//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// Two real daemons and linked worktrees share primary Git configuration. One
// already owns the legacy remote; the other must never redirect or remove it.
// Ordinary AXI and nonce-bound (automatic proxy) submissions both run gates.
func TestIsolatedTaskGates(t *testing.T) {
	first := NewHarness(t, SetupOpts{Agent: "codex"})
	if out, err := first.Run("init", "--no-user-skill"); err != nil {
		t.Fatalf("first init: %v\n%s", err, out)
	}
	second := NewHarness(t, SetupOpts{Agent: "codex"})
	second.WorkDir = first.WorkDir
	configFile := filepath.Join(first.WorkDir, ".git", "config")
	before, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	type task struct {
		h                 *Harness
		dir, branch, head string
		env               map[string]string
		cmd               *exec.Cmd
		output            bytes.Buffer
	}
	tasks := []*task{
		{h: first, branch: "task/ordinary-isolated"},
		{h: second, branch: "task/proof-isolated"},
	}
	for i, task := range tasks {
		task.dir = filepath.Join(t.TempDir(), "linked")
		if out, err := first.runGit(ctx, first.WorkDir, "worktree", "add", "-b", task.branch, task.dir, "main"); err != nil {
			t.Fatalf("linked worktree: %v: %s", err, out)
		}
		t.Cleanup(func() {
			_, _ = first.runGit(context.Background(), first.WorkDir, "worktree", "remove", "--force", task.dir)
		})
		task.env = map[string]string{"NM_HOME": task.h.NMHome, "HOME": task.h.HomeDir, "FAKEAGENT_LOG": task.h.AgentLog}
		for n := 0; n < 2; n++ {
			if out, err := task.h.RunInDirWithEnv(task.dir, task.env, "init", "--isolated", "--no-user-skill"); err != nil {
				t.Fatalf("task %d init/refresh: %v\n%s", i, err, out)
			}
		}
		file := filepath.Join(task.dir, "isolated.txt")
		if err := os.WriteFile(file, []byte(task.branch+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "isolated.txt"}, {"commit", "-m", "document isolated task"}} {
			if out, err := first.runGit(ctx, task.dir, args...); err != nil {
				t.Fatalf("commit: %v: %s", err, out)
			}
		}
		out, err := first.runGit(ctx, task.dir, "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		task.head = strings.TrimSpace(string(out))
		args := []string{"axi", "run", "--intent", "Verify task-owned gate isolation", "--skip", "test,pr,ci", "--wait", "45s"}
		if i == 1 {
			args = append(args, "--launch-nonce", "isolated-proof", "--validation-generation", "isolated-v1")
		}
		task.cmd = exec.CommandContext(ctx, task.h.NMBin, args...)
		task.cmd.Dir = task.dir
		task.cmd.Env = mergedEnv(os.Environ(), task.env)
		task.cmd.Stdout, task.cmd.Stderr = &task.output, &task.output
	}
	// Start both before waiting, proving the gate homes can be active together.
	for _, task := range tasks {
		if err := task.cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range tasks {
		if err := task.cmd.Wait(); err != nil {
			t.Fatalf("%s: %v\n%s", task.branch, err, task.output.String())
		}
		runs := task.h.Runs()
		if len(runs) != 1 || runs[0].Branch != task.branch || runs[0].Status != types.RunCompleted || runs[0].SubmittedHeadSHA == nil || *runs[0].SubmittedHeadSHA != task.head {
			t.Fatalf("%s received wrong validation: %+v", task.branch, runs)
		}
		result := task.h.RunInfo(runs[0].ID)
		for _, name := range []types.StepName{types.StepReview, types.StepDocument, types.StepLint} {
			found := false
			for _, step := range result.Steps {
				if step.StepName == name {
					found = true
					if step.Status != types.StepStatusCompleted {
						t.Fatalf("%s %s gate status=%s", task.branch, name, step.Status)
					}
				}
			}
			if !found {
				t.Fatalf("%s missing %s gate", task.branch, name)
			}
		}
	}
	if out, err := second.RunInDirWithEnv(tasks[1].dir, tasks[1].env, "eject"); err != nil {
		t.Fatalf("second eject: %v\n%s", err, out)
	}
	if out, err := first.RunInDirWithEnv(tasks[0].dir, tasks[0].env, "daemon", "status"); err != nil || !strings.Contains(out, "daemon running") {
		t.Fatalf("first task daemon disturbed: %v: %s", err, out)
	}
	after, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("isolated tasks changed the shared repository configuration")
	}
}
