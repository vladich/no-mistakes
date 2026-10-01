//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agentgitproxy"
	"github.com/kunchenguid/no-mistakes/internal/testgit"
)

// A native creator is required here. Receipt parser mocks are covered by the
// ordinary suite; this journey proves a real CoW task and daemon worktree.
func TestAgentGitProxyAutomaticPublicationJourney(t *testing.T) {
	tool := "cowtree"
	if runtime.GOOS == "linux" {
		tool = "git-cow-worktree"
	}
	creator, err := exec.LookPath(tool)
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("native strict CoW creator is not installed on this test capability")
	}
	realGit, err := testgit.RealGit()
	if err != nil {
		t.Fatal(err)
	}
	h := NewHarness(t, SetupOpts{Agent: "codex"})
	mustGit := func(dir string, args ...string) string {
		t.Helper()
		out, err := h.runGit(context.Background(), dir, args...)
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// Real upload-pack/receive-pack with a local SSH transport. No forge or
	// user credentials are needed, and origin still has its GitLab identity.
	ssh := filepath.Join(h.BinDir, "fixture-ssh")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n*git-upload-pack*) exec %q upload-pack %q;;\n*git-receive-pack*) exec %q receive-pack %q;;\n*) exit 91;;\nesac\n", realGit, h.UpstreamDir, realGit, h.UpstreamDir)
	if err := os.WriteFile(ssh, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	mustGit(h.WorkDir, "config", "--global", "core.sshCommand", ssh)
	origin := "git@gitlab.test:ater/proxy.git"
	mustGit(h.WorkDir, "remote", "set-url", "origin", origin)
	mustGit(h.UpstreamDir, "config", "receive.advertisePushOptions", "true")
	// The default/release branch owns trusted repo settings, while main owns
	// this project's integration CI. The release snapshot has no CI file.
	mustGit(h.UpstreamDir, "branch", "release", "refs/heads/main")
	mustGit(h.UpstreamDir, "symbolic-ref", "HEAD", "refs/heads/release")
	workflow := fmt.Sprintf("workflow:\n  rules:\n    - if: '%s'\n      when: never\n    - if: '%s'\n      when: never\n    - if: '$CI_COMMIT_BRANCH == \"main\"'\nverify:\n  script: ['true']\n", agentgitproxy.SuppressMR, agentgitproxy.SuppressTask)
	h.CommitChange("main", ".gitlab-ci.yml", workflow, "declare single integration CI owner")
	mustGit(h.WorkDir, "push", "origin", "main")
	glab := "#!/bin/sh\ncase \"$1 $2\" in\n'auth status') exit 0;;\n'mr list') printf '[]\\n';;\n*) exit 92;;\nesac\n"
	if err := os.WriteFile(filepath.Join(h.BinDir, "glab"), []byte(glab), 0o700); err != nil {
		t.Fatal(err)
	}
	goal := "Document the automatic author-side Git publication canary."
	claimFile := filepath.Join(h.NMHome, "claim.json")
	claim := map[string]any{"project": "test", "session_id": "session", "ticket_id": 42,
		"claim_held_by_session": true, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		"renewal": map[string]any{"state": "healthy"}}
	claimBytes, _ := json.Marshal(claim)
	if err := os.WriteFile(claimFile, claimBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	c := agentgitproxy.Context{Protocol: agentgitproxy.Protocol, Project: "test", CheckoutRoot: h.WorkDir,
		IntegrationBranch: "main", Intent: goal, Assignment: "42", AssignmentField: "ticket_id",
		ClaimFile: claimFile, SessionID: "session", UpstreamSHA256: agentgitproxy.URLDigest(origin)}
	publisher := filepath.Join(h.NMHome, "publisher.py")
	publisherBytes := []byte(fmt.Sprintf("AUTHOR_VALIDATION_PROTOCOL = %q\nimport os, subprocess\nhead = subprocess.check_output([%q, 'rev-parse', 'HEAD'], text=True).strip()\nassert os.environ['ATER_SESSION_PUBLISH_VALIDATED_SHA'] == head\nprint('accepted ' + head)\n", agentgitproxy.Protocol, realGit))
	if err := os.WriteFile(publisher, publisherBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	c.PublisherPath, c.PublisherSHA256 = publisher, fmt.Sprintf("%x", sha256.Sum256(publisherBytes))
	b, _ := json.Marshal(c)
	contextFile := filepath.Join(h.NMHome, "git-proxy-context.json")
	if err := os.WriteFile(contextFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	h.globalConfigExtra = fmt.Sprintf("agent_git_proxy:\n  git_binary: %q\n  context_file: %q\n", realGit, contextFile)
	h.writeGlobalConfig()
	if output, err := h.Run("agent-push", "--", "origin", "main"); err == nil || (!strings.Contains(string(output), "no completed task validation") && !strings.Contains(string(output), "repo not initialized")) {
		t.Fatalf("unvalidated canonical publication admitted: %v: %s", err, output)
	}
	foreignRemote := filepath.Join(h.NMHome, "another-active-task.git")
	mustGit(h.WorkDir, "remote", "add", "no-mistakes", foreignRemote)
	sharedBefore := mustGit(h.WorkDir, "config", "--get-regexp", "^remote\\.no-mistakes\\.")
	t.Setenv("ATER_COW_COWTREE", creator)
	t.Setenv("ATER_COW_ORIGINAL_PATH", os.Getenv("PATH"))
	branch := "task/automatic-proxy"
	head := h.CommitChange(branch, "docs/proxy-canary.md", "# Automatic publication canary\n", "document proxy canary")
	mustGit(h.WorkDir, "checkout", "main")
	task := filepath.Join(h.t.TempDir(), "task")
	child := exec.Command(creator, "add", task, branch)
	child.Dir = h.WorkDir
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("strict CoW creation: %v: %s", err, output)
	}
	t.Cleanup(func() { _, _ = h.runGit(context.Background(), h.WorkDir, "worktree", "remove", "--force", task) })
	output, err := h.RunInDir(task, "agent-push", "--", "-u", "origin", "HEAD")
	if err != nil {
		h.dumpDebugState()
		t.Fatalf("automatic task push: %v: %s", err, output)
	}
	if actual := h.UpstreamBranchSHA(branch); actual != head {
		t.Fatalf("published %s, want %s", actual, head)
	}
	sharedAfter := mustGit(h.WorkDir, "config", "--get-regexp", "^remote\\.no-mistakes\\.")
	if sharedBefore != sharedAfter {
		t.Fatal("automatic proxy changed another task's shared remote")
	}
	first := h.Runs()
	if len(first) != 1 {
		t.Fatalf("got %d runs, want one", len(first))
	}
	invocations := len(h.AgentInvocations())
	if output, err := h.RunInDir(task, "agent-push", "--", "origin", "HEAD"); err != nil {
		t.Fatalf("retry: %v: %s", err, output)
	}
	if len(h.Runs()) != 1 || len(h.AgentInvocations()) != invocations {
		t.Fatal("identical push reran validation")
	}
	mustGit(h.WorkDir, "merge", "--ff-only", head)
	if output, err := h.Run("agent-publish", "--hosted"); err != nil || !strings.Contains(string(output), "accepted "+head) {
		t.Fatalf("bound hosted publisher: %v: %s", err, output)
	}
	if err := os.WriteFile(publisher, []byte("tampered publisher\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := h.Run("agent-publish", "--hosted"); err == nil {
		t.Fatalf("changed publisher executed: %s", output)
	}
	if err := os.WriteFile(publisher, publisherBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := h.Run("agent-push", "--", "origin", "main"); err != nil {
		t.Fatalf("canonical publication: %v: %s", err, output)
	}
	if actual := h.UpstreamBranchSHA("main"); actual != head {
		t.Fatalf("canonical published %s, want %s", actual, head)
	}
	if len(h.Runs()) != 1 {
		t.Fatal("canonical publication reran no-mistakes")
	}
	// An old successful run never admits a new canonical commit.
	h.CommitChange("main", "unvalidated.txt", "unvalidated\n", "unvalidated change")
	if output, err := h.Run("agent-push", "--", "origin", "main"); err == nil || !strings.Contains(string(output), "no completed task validation") {
		t.Fatalf("changed canonical head reused validation: %v: %s", err, output)
	}
	if actual := h.UpstreamBranchSHA("main"); actual != head {
		t.Fatalf("unvalidated head reached the remote: %s", actual)
	}
	// Revocation is checked again even for an otherwise reusable task result.
	claim["claim_held_by_session"] = false
	claimBytes, _ = json.Marshal(claim)
	if err := os.WriteFile(claimFile, claimBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := h.RunInDir(task, "agent-push", "--", "origin", "HEAD"); err == nil {
		t.Fatalf("revoked claim reused validation: %s", output)
	}
	claim["claim_held_by_session"] = true
	claimBytes, _ = json.Marshal(claim)
	if err := os.WriteFile(claimFile, claimBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	old := mustGit(h.UpstreamDir, "rev-parse", head+"^")
	mustGit(h.UpstreamDir, "update-ref", "refs/heads/"+branch, old)
	if output, err := h.RunInDir(task, "agent-push", "--", "origin", "--delete", branch); err == nil {
		t.Fatalf("cleanup deleted a changed remote head: %s", output)
	}
	mustGit(h.UpstreamDir, "update-ref", "refs/heads/"+branch, head)
	if output, err := h.RunInDir(task, "agent-push", "--", "origin", "--delete", branch); err != nil {
		t.Fatalf("validated cleanup: %v: %s", err, output)
	}
	if actual := mustGit(h.UpstreamDir, "for-each-ref", "--format=%(refname)", "refs/heads/"+branch); actual != "" {
		t.Fatalf("canary branch remains after cleanup: %s", actual)
	}
	if len(h.Runs()) != 1 {
		t.Fatal("cleanup reran no-mistakes")
	}
}
