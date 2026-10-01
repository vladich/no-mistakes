package steps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

// Use a real receive-pack boundary: the controller must publish the exact
// validated object, suppress branch CI, and avoid re-entering the PATH proxy.
func TestAgentGitProxyFinalPushUsesRealGitAndExactCommit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private agent Git proxy is macOS/Linux only")
	}
	if testGitErr != nil {
		t.Fatal(testGitErr)
	}
	root := t.TempDir()
	work, remote := filepath.Join(root, "work"), filepath.Join(root, "remote.git")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	realGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(testGitExecutable, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	realGit(work, "init", "--initial-branch=main")
	realGit(work, "config", "user.name", "Proxy Test")
	realGit(work, "config", "user.email", "proxy@example.invalid")
	realGit(work, "init", "--bare", remote)
	realGit(work, "--git-dir="+remote, "config", "receive.advertisePushOptions", "true")
	hook := "#!/bin/sh\nprintf '%s\\n' \"$GIT_PUSH_OPTION_COUNT\" \"$GIT_PUSH_OPTION_0\" > \"$PWD/observed-push-options\"\n"
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(work, "change.txt")
	if err := os.WriteFile(file, []byte("validated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realGit(work, "add", "change.txt")
	realGit(work, "commit", "-m", "validated")
	validated := realGit(work, "rev-parse", "HEAD")
	if err := os.WriteFile(file, []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realGit(work, "commit", "-am", "later unvalidated head")
	proxyDir := filepath.Join(root, "proxy")
	if err := os.Mkdir(proxyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxyDir, "git"), []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sctx := &pipeline.StepContext{Ctx: context.Background(), WorkDir: work,
		Config: &config.Config{AgentGitProxy: &config.AgentGitProxyConfig{GitBinary: testGitExecutable}},
		Env:    []string{"PATH=" + proxyDir + string(os.PathListSeparator) + os.Getenv("PATH")}}
	if err := stepGitPushCommit(sctx, remote, validated, "refs/heads/task/test", "", false); err != nil {
		t.Fatal(err)
	}
	if actual := realGit(work, "--git-dir="+remote, "rev-parse", "refs/heads/task/test"); actual != validated {
		t.Fatalf("remote head %s, want exact validated head %s", actual, validated)
	}
	options, err := os.ReadFile(filepath.Join(remote, "observed-push-options"))
	if err != nil || string(options) != "1\nci.skip\n" {
		t.Fatalf("push options %q, error %v", options, err)
	}
}
