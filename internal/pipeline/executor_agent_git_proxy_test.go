package pipeline

import (
	"context"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// Prove the durable execution result, including a caller trying to bypass
// author validation with --skip. External CI is the only full test owner.
func TestAgentGitProxyExecutorCannotSkipAuthorGates(t *testing.T) {
	database, p, run, repo := setupTest(t)
	names := []types.StepName{types.StepIntent, types.StepRebase, types.StepReview,
		types.StepTest, types.StepDocument, types.StepLint, types.StepPush, types.StepPR, types.StepCI}
	var steps []Step
	for _, name := range names {
		steps = append(steps, newPassStep(name))
	}
	cfg := config.Merge(config.DefaultGlobalConfig(), &config.RepoConfig{})
	cfg.AgentGitProxy = &config.AgentGitProxyConfig{}
	executor := NewExecutor(database, p, cfg, nil, steps, nil)
	executor.SetSkippedSteps(names)
	if err := executor.Execute(context.Background(), run, repo, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	results, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(names) {
		t.Fatalf("got %d durable steps, want %d", len(results), len(names))
	}
	for _, result := range results {
		want := types.StepStatusCompleted
		if result.StepName == types.StepTest || result.StepName == types.StepPR || result.StepName == types.StepCI {
			want = types.StepStatusSkipped
		}
		if result.Status != want {
			t.Errorf("%s status %s, want %s", result.StepName, result.Status, want)
		}
	}
}
