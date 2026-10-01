package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"github.com/kunchenguid/no-mistakes/internal/agentgitproxy"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/shellenv"
	"github.com/kunchenguid/no-mistakes/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"os"
	"os/exec"
)

func newAgentPublishCmd() *cobra.Command {
	return &cobra.Command{Use: "agent-publish", Hidden: true, DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || args[0] != "--hosted" {
				return fmt.Errorf("agent-publish requires the launcher's hosted publisher")
			}
			return runAgentPublish(cmd, nil, true)
		}}
}

// Adds author validation admission to the existing canonical publication path.
// It grants no independent reviewer authority or branch-protection changes.
func runAgentPublish(cmd *cobra.Command, args []string, hosted bool) error {
	p, err := paths.New()
	if err != nil {
		return err
	}
	cfg, err := config.LoadGlobal(p.ConfigFile())
	if err != nil {
		return err
	}
	if cfg.AgentGitProxy == nil {
		return fmt.Errorf("automatic publication is not configured")
	}
	c, err := agentgitproxy.LoadContext(cfg.AgentGitProxy.ContextFile)
	if err != nil {
		return err
	}
	ctx := git.WithExecutable(cmd.Context(), cfg.AgentGitProxy.GitBinary)
	root := worktrees.Canonical(c.CheckoutRoot)
	callerRoot, err := git.FindMainRepoRoot(".")
	if err != nil || worktrees.Canonical(callerRoot) != root {
		return fmt.Errorf("canonical publication belongs to another checkout")
	}
	branch, err := git.CurrentBranch(ctx, root)
	if err != nil || branch != c.IntegrationBranch {
		return fmt.Errorf("canonical checkout is not on its admitted integration branch")
	}
	status, err := git.RunRaw(ctx, root, "status", "--porcelain", "--untracked-files=normal")
	if err != nil || len(status) != 0 {
		return fmt.Errorf("canonical publication requires a clean integration checkout")
	}
	head, err := git.HeadSHA(ctx, root)
	if err != nil {
		return err
	}
	origin, err := git.GetRemoteURL(ctx, root, "origin")
	if err != nil || agentgitproxy.URLDigest(origin) != c.UpstreamSHA256 {
		return fmt.Errorf("canonical publication destination changed")
	}
	remote, upstream := "origin", false
	if !hosted {
		callerBranch, err := git.CurrentBranch(ctx, ".")
		if err != nil || callerBranch != branch {
			return fmt.Errorf("local canonical push must run from its integration checkout")
		}
		remote, upstream, err = agentgitproxy.ParseBranchPush(args, branch)
		if err != nil {
			return err
		}
		requested, err := git.GetRemoteURL(ctx, root, remote)
		if err != nil || requested != origin {
			return fmt.Errorf("canonical publication refuses another remote")
		}
	}
	env, err := openAxiEnv(false)
	if err != nil {
		return err
	}
	defer env.close()
	runs, err := env.d.GetPublishedTaskRunsByHead(env.repo.ID, head)
	if err != nil {
		return err
	}
	validated := false
	for _, run := range runs {
		steps, err := env.d.GetStepsByRun(run.ID)
		if err != nil {
			return err
		}
		if _, err := agentgitproxy.ValidateReceipt(c, run, steps, head, run.Branch); err == nil {
			validated = true
			break
		}
	}
	if !validated {
		return fmt.Errorf("canonical head has no completed task validation; push its task branch through the proxy, then integrate the returned validated head")
	}
	fresh, err := agentgitproxy.LoadContext(cfg.AgentGitProxy.ContextFile)
	if err != nil {
		return err
	}
	if *fresh != *c {
		return fmt.Errorf("canonical publication assignment changed during validation")
	}
	if hosted {
		if c.PublisherPath == "" {
			return fmt.Errorf("hosted canonical publisher was not bound at startup")
		}
		info, err := os.Lstat(c.PublisherPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("hosted canonical publisher is unsafe")
		}
		f, err := os.Open(c.PublisherPath)
		if err != nil {
			return err
		}
		b, readErr := io.ReadAll(io.LimitReader(f, 65537))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || len(b) > 65536 || fmt.Sprintf("%x", sha256.Sum256(b)) != c.PublisherSHA256 {
			return fmt.Errorf("hosted canonical publisher changed after admission")
		}
		if !bytes.Contains(b, []byte(`AUTHOR_VALIDATION_PROTOCOL = "`+agentgitproxy.Protocol+`"`)) {
			return fmt.Errorf("hosted canonical publisher cannot bind the validated revision")
		}
		child := exec.CommandContext(ctx, "/usr/bin/python3", c.PublisherPath)
		child.Dir, child.Env = root, append(os.Environ(), "ATER_SESSION_PUBLISH_VALIDATED_SHA="+head)
		child.Stdout, child.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
		shellenv.ConfigureShellCommand(child)
		return shellenv.RunShellCommand(child)
	}
	pushArgs := []string{"push"}
	if upstream {
		pushArgs = append(pushArgs, "--set-upstream")
	}
	pushArgs = append(pushArgs, remote, head+":refs/heads/"+branch)
	output, err := git.RunRaw(ctx, root, pushArgs...)
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(output)
	return err
}
