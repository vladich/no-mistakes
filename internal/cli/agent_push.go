package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agentgitproxy"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/worktrees"
	"github.com/spf13/cobra"
)

// The accepted launcher invokes this command through its Git dispatcher.
// Flags cannot change intent, gates, source head, integration target or publisher.
func newAgentPushCmd() *cobra.Command {
	return &cobra.Command{
		Use: "agent-push -- [git push arguments]", Hidden: true,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			return runAgentPush(cmd, args)
		},
	}
}

func runAgentPush(cmd *cobra.Command, args []string) error {
	p, err := paths.New()
	if err != nil {
		return err
	}
	cfg, err := config.LoadGlobal(p.ConfigFile())
	if err != nil {
		return err
	}
	if cfg.AgentGitProxy == nil {
		return fmt.Errorf("agent Git proxy is not configured by the accepted launcher")
	}
	c, err := agentgitproxy.LoadContext(cfg.AgentGitProxy.ContextFile)
	if err != nil {
		return err
	}
	if err := validateAgentGitExecutable(cfg.AgentGitProxy.GitBinary); err != nil {
		return err
	}
	ctx := git.WithExecutable(cmd.Context(), cfg.AgentGitProxy.GitBinary)
	cmd.SetContext(ctx)
	branch, err := git.CurrentBranch(ctx, ".")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(branch, "task/") {
		return runAgentPublish(cmd, args, false)
	}
	remote, upstream, err := agentgitproxy.ParsePush(args, branch)
	deleteRemote, deleting, deleteErr := agentgitproxy.ParseTaskDelete(args, branch)
	if deleting {
		remote, upstream, err = deleteRemote, false, deleteErr
	}
	if err != nil {
		return err
	}
	root, err := git.FindMainRepoRoot(".")
	if err != nil {
		return err
	}
	root = worktrees.Canonical(root)
	acceptedRoot := worktrees.Canonical(c.CheckoutRoot)
	if root != acceptedRoot {
		return fmt.Errorf("agent Git proxy task context belongs to another checkout")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	workingRoot, err := git.Run(ctx, ".", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	workingRoot = worktrees.Canonical(workingRoot)
	if workingRoot == root {
		return fmt.Errorf("agent Git proxy requires a separate task worktree")
	}
	origin, err := git.GetRemoteURL(ctx, cwd, "origin")
	if err != nil {
		return fmt.Errorf("agent Git proxy requires a registered origin")
	}
	if agentgitproxy.URLDigest(origin) != c.UpstreamSHA256 {
		return fmt.Errorf("agent Git proxy origin changed after task admission")
	}
	requested, err := git.GetRemoteURL(ctx, cwd, remote)
	if err != nil || requested != origin {
		return fmt.Errorf("agent Git proxy refuses an alternate publication remote")
	}
	status, err := git.RunRaw(ctx, cwd, "status", "--porcelain", "--untracked-files=normal")
	if err != nil || len(status) != 0 {
		return fmt.Errorf("agent Git proxy requires committed changes and a clean task worktree")
	}
	submitted, err := git.HeadSHA(ctx, cwd)
	if err != nil {
		return err
	}
	init := newInitCmd()
	init.SetContext(ctx)
	init.SetOut(cmd.OutOrStdout())
	init.SetErr(cmd.ErrOrStderr())
	if err := init.Flags().Set("no-user-skill", "true"); err != nil {
		return err
	}
	if err := init.RunE(init, nil); err != nil {
		return err
	}
	env, err := openAxiEnv(false)
	if err != nil {
		return err
	}
	defer env.close()
	// A repeated push of an already published head must not run the agent
	// gates again. The guarded sync below still proves live remote equality.
	var receipt *agentgitproxy.Receipt
	runs, err := env.d.GetRunsByRepoHead(env.repo.ID, branch, submitted)
	if err != nil {
		return err
	}
	for _, previous := range runs {
		steps, err := env.d.GetStepsByRun(previous.ID)
		if err != nil {
			return err
		}
		if candidate, err := agentgitproxy.ValidateReceipt(c, previous, steps, submitted, branch); err == nil {
			receipt = candidate
			break
		}
	}
	if receipt == nil {
		if deleting {
			return fmt.Errorf("task cleanup requires its completed author validation")
		}
		nonce := agentgitproxy.LaunchNonce(c, submitted, branch)
		runCmd := newAxiRunCmd()
		runCmd.SetContext(ctx)
		runCmd.SetOut(cmd.OutOrStdout())
		runCmd.SetErr(cmd.ErrOrStderr())
		for key, value := range map[string]string{"intent": c.Intent, "skip": "test,pr,ci", "base-branch": c.IntegrationBranch,
			"launch-nonce": nonce, "validation-generation": agentgitproxy.ValidationGeneration} {
			if err := runCmd.Flags().Set(key, value); err != nil {
				return err
			}
		}
		if err := runCmd.RunE(runCmd, nil); err != nil {
			return err
		}
		run, err := env.d.GetRunByLaunchNonce(env.repo.ID, branch, nonce)
		if err != nil {
			return err
		}
		if run == nil {
			return fmt.Errorf("agent Git proxy received no durable validation run")
		}
		steps, err := env.d.GetStepsByRun(run.ID)
		if err != nil {
			return err
		}
		receipt, err = agentgitproxy.ValidateReceipt(c, run, steps, submitted, branch)
		if err != nil {
			return err
		}
	}
	if deleting {
		fresh, err := agentgitproxy.LoadContext(cfg.AgentGitProxy.ContextFile)
		if err != nil {
			return err
		}
		if *fresh != *c {
			return fmt.Errorf("task cleanup assignment changed")
		}
		ref := "refs/heads/" + branch
		current, err := git.Run(ctx, cwd, "ls-remote", remote, ref)
		if err != nil {
			return err
		}
		if strings.TrimSpace(current) == "" {
			return nil
		}
		fields := strings.Fields(current)
		if len(fields) != 2 || fields[0] != receipt.PublishedSHA || fields[1] != ref {
			return fmt.Errorf("task cleanup refuses a remote head changed after validation")
		}
		if _, err := git.RunRaw(ctx, cwd, "push", "--force-with-lease="+ref+":"+receipt.PublishedSHA, remote, ":"+ref); err != nil {
			return err
		}
		current, err = git.Run(ctx, cwd, "ls-remote", remote, ref)
		if err != nil || strings.TrimSpace(current) != "" {
			return fmt.Errorf("task cleanup could not verify remote deletion")
		}
		return nil
	}
	// Sync uses the existing custody and live-remote checks; it never substitutes
	// an unconditional reset for an author worktree that changed during the run.
	syncCmd := newAxiSyncCmd()
	syncCmd.SetContext(ctx)
	syncCmd.SetOut(cmd.OutOrStdout())
	syncCmd.SetErr(cmd.ErrOrStderr())
	if err := syncCmd.RunE(syncCmd, nil); err != nil {
		return err
	}
	head, err := git.HeadSHA(ctx, cwd)
	if err != nil || head != receipt.PublishedSHA {
		return fmt.Errorf("agent Git proxy could not reconcile the validated task head")
	}
	if upstream {
		// The daemon publishes by URL; it cannot update the author's tracking
		// ref. Fetch it before configuring upstream, then verify exact equality.
		if err := git.FetchRemoteBranch(ctx, cwd, remote, branch); err != nil {
			return err
		}
		remoteHead, err := git.Run(ctx, cwd, "rev-parse", "refs/remotes/"+remote+"/"+branch)
		if err != nil || remoteHead != receipt.PublishedSHA {
			return fmt.Errorf("agent Git proxy upstream moved after validation")
		}
		if _, err := git.Run(ctx, cwd, "branch", "--set-upstream-to="+remote+"/"+branch, branch); err != nil {
			return err
		}
	}
	b, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	receiptDir := filepath.Join(p.Root(), "git-push-receipts")
	if err := os.MkdirAll(receiptDir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(receiptDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("agent Git proxy receipt directory is not private")
	}
	f, err := os.CreateTemp(receiptDir, ".push-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(receiptDir, receipt.RunID+".json")); err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(b))
	return err
}
