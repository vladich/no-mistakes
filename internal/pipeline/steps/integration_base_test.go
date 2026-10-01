package steps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// master lacks a change already integrated into dev. Only task.txt belongs to
// this task, even when its submitted base still points at master.
func integrationBaseFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	dir, master, dev := setupGitRepo(t)
	gitCmd(t, dir, "branch", "master", master)
	gitCmd(t, dir, "branch", "dev", dev)
	if err := os.WriteFile(filepath.Join(dir, "task.txt"), []byte("task change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "task.txt")
	gitCmd(t, dir, "commit", "-m", "task change")
	return dir, master, dev, gitCmd(t, dir, "rev-parse", "HEAD")
}

func integrationScopedSteps() []pipeline.Step {
	return []pipeline.Step{
		&ReviewStep{}, &DocumentStep{}, &LintStep{}, &TestStep{},
		&CustomGateStep{Gate: config.Gate{Name: "scope", After: types.StepReview, Command: "exit 0"}},
	}
}

func TestBranchScopedSteps_UseIntegrationBase(t *testing.T) {
	for _, step := range integrationScopedSteps() {
		for _, selection := range []string{"config", "run", "default"} {
			t.Run(string(step.Name())+"/"+selection, func(t *testing.T) {
				t.Parallel()
				dir, master, dev, head := integrationBaseFixture(t)
				stop := errors.New("stop after observing the scoped agent prompt")
				ag := &mockAgent{name: "mock", runFn: func(_ context.Context, _ agent.RunOpts) (*agent.Result, error) { return nil, stop }}
				sctx := newTestContextWithDBRecords(t, ag, dir, master, head, config.Commands{})
				sctx.Repo.DefaultBranch = "master"
				want := dev
				switch selection {
				case "config":
					sctx.Config.PR.BaseBranch = "dev"
				case "run":
					sctx.Config.PR.BaseBranch = "missing-config-base"
					branch := "dev"
					sctx.Run.PRBaseBranch = &branch
				case "default":
					want = master
				}
				if _, ok := step.(*CustomGateStep); ok {
					sctx.Fixing = true
				}
				_, err := step.Execute(sctx)
				if !errors.Is(err, stop) {
					t.Fatalf("Execute = %v, want observed agent turn", err)
				}
				if len(ag.calls) != 1 {
					t.Fatalf("agent calls = %d, want 1", len(ag.calls))
				}
				prompt := ag.calls[0].Prompt
				if !strings.Contains(prompt, "- base commit: "+want) {
					t.Fatalf("agent prompt does not use selected base %s", want)
				}
				if want == dev && strings.Contains(prompt, master) {
					t.Fatal("agent prompt leaked forge-default change range")
				}
				if step.Name() == types.StepReview && want == dev {
					if !strings.Contains(prompt, "\n- task.txt\n") || strings.Contains(prompt, "\n- feature.txt\n") {
						t.Fatal("review coverage includes changes already integrated into dev")
					}
				}
				// Verify the selected real-Git range excludes the already integrated file.
				paths := gitCmd(t, dir, "diff", "--name-only", want, head)
				if want == dev && paths != "task.txt" {
					t.Fatalf("task range = %q, want task.txt only", paths)
				}
				if (step.Name() == types.StepReview || step.Name() == types.StepDocument) && want == dev && !strings.Contains(prompt, "- default branch: dev") {
					t.Fatal("prompt branch label disagrees with selected integration base")
				}
			})
		}
	}
}

func TestBranchScopedSteps_RefuseMissingIntegrationBase(t *testing.T) {
	for _, step := range integrationScopedSteps() {
		t.Run(string(step.Name()), func(t *testing.T) {
			t.Parallel()
			dir, master, _, head := integrationBaseFixture(t)
			ag := &mockAgent{name: "mock"}
			sctx := newTestContextWithDBRecords(t, ag, dir, master, head, config.Commands{})
			sctx.Repo.DefaultBranch = "master"
			sctx.Config.PR.BaseBranch = "missing-integration-base"
			if _, ok := step.(*CustomGateStep); ok {
				sctx.Fixing = true
			}
			_, err := step.Execute(sctx)
			if err == nil || !strings.Contains(err.Error(), "missing-integration-base") {
				t.Fatalf("Execute = %v, want integration-base fetch refusal", err)
			}
			if len(ag.calls) != 0 {
				t.Fatal("agent launched without verified integration base")
			}
			if got := gitCmd(t, dir, "rev-parse", "HEAD"); got != head {
				t.Fatal("failed base fetch mutated HEAD")
			}
		})
	}
}
