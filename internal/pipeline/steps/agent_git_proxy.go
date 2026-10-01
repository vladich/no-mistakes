package steps

import (
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agentgitproxy"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/kunchenguid/no-mistakes/internal/worktrees"
)

// An author session publishes task branches. MR creation, independent review,
// CI selection and integration belong to the external MR orchestrator.
// Existing MRs are valid: a fix must be able to update the same MR branch.
func assertAgentGitProxyPublication(sctx *pipeline.StepContext, branch, pushURL, head string) error {
	if sctx.Config == nil || sctx.Config.AgentGitProxy == nil {
		return nil
	}
	if err := sctx.Config.AgentGitProxy.Validate(); err != nil {
		return err
	}
	if scm.DetectProvider(pushURL) != scm.ProviderGitLab {
		return fmt.Errorf("agent Git proxy publication requires the registered GitLab project")
	}
	if !strings.HasPrefix(branch, "task/") || len(branch) == len("task/") {
		return fmt.Errorf("agent_git_proxy publishes only task/ branches")
	}
	context, err := agentgitproxy.LoadContext(sctx.Config.AgentGitProxy.ContextFile)
	if err != nil {
		return err
	}
	if err := agentgitproxy.ValidateLaunch(context, sctx.Run); err != nil {
		return err
	}
	if sctx.Run.Intent == nil || *sctx.Run.Intent != context.Intent {
		return fmt.Errorf("agent Git proxy task intent changed during validation")
	}
	if sctx.Run.PRBaseBranch == nil || *sctx.Run.PRBaseBranch != context.IntegrationBranch {
		return fmt.Errorf("agent Git proxy integration target changed during validation")
	}
	if agentgitproxy.URLDigest(pushURL) != context.UpstreamSHA256 {
		return fmt.Errorf("agent Git proxy refuses a changed publication destination")
	}
	if worktrees.Canonical(sctx.Repo.WorkingPath) != worktrees.Canonical(context.CheckoutRoot) {
		return fmt.Errorf("agent Git proxy task context changed checkout during validation")
	}
	if err := assertAgentGitProxyCI(sctx, context.IntegrationBranch, pushURL, head); err != nil {
		return err
	}
	steps, err := sctx.DB.GetStepsByRun(sctx.Run.ID)
	if err != nil {
		return fmt.Errorf("read agent Git proxy gate evidence: %w", err)
	}
	seen := make(map[types.StepName]bool)
	for _, step := range steps {
		switch step.StepName {
		case types.StepPush, types.StepPR, types.StepCI:
			continue // Publication and the deliberately skipped later steps.
		case types.StepTest:
			if step.Status != types.StepStatusSkipped || step.Error != nil || step.OverrideReason != nil {
				return fmt.Errorf("agent Git proxy requires the external test owner")
			}
			continue
		}
		if err := agentgitproxy.ValidateGate(step); err != nil {
			return err
		}
		seen[step.StepName] = true
	}
	for _, name := range []types.StepName{types.StepIntent, types.StepRebase, types.StepReview, types.StepDocument, types.StepLint} {
		if !seen[name] {
			return fmt.Errorf("agent Git proxy is missing %s evidence", name)
		}
	}
	return nil
}

func assertAgentGitProxyCI(sctx *pipeline.StepContext, integrationBranch, pushURL, head string) error {
	// CI belongs to the integration branch. A separate release/default branch
	// may legitimately have no CI file; it remains the repo-config authority.
	ciRef := "refs/no-mistakes/agent-ci/" + sctx.Run.ID
	if _, err := stepGitRun(sctx, "fetch", "--no-tags", "--no-write-fetch-head", pushURL,
		"+refs/heads/"+integrationBranch+":"+ciRef); err != nil {
		return fmt.Errorf("fetch integration CI authority: %w", err)
	}
	base, err := stepGitRun(sctx, "rev-parse", "--verify", ciRef+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve integration CI authority: %w", err)
	}
	base = strings.TrimSpace(base)
	if _, err := stepGitRun(sctx, "merge-base", "--is-ancestor", base, head); err != nil {
		return fmt.Errorf("integration advanced beyond the validated candidate; rebase and validate again: %w", err)
	}
	for _, revision := range []string{base, head} {
		workflow, err := stepGitRun(sctx, "show", revision+":.gitlab-ci.yml")
		if err != nil {
			return fmt.Errorf("read project-owned CI workflow: %w", err)
		}
		if err := agentgitproxy.ValidateIntegrationWorkflow([]byte(workflow), integrationBranch); err != nil {
			return err
		}
	}
	return nil
}
