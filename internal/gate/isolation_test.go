package gate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/paths"
)

func TestEjectPreservesAnotherTasksRemote(t *testing.T) {
	work := setupTestRepo(t)
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t, p)
	ctx := context.Background()
	if _, _, err := Init(ctx, d, p, work); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(t.TempDir(), "active-task.git")
	if err := gitpkg.EnsureRemote(ctx, work, RemoteName, foreign); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(work, ".git", "config")
	before, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Eject(ctx, d, p, work); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("eject altered another task's shared remote configuration")
	}
}

func TestIsolatedInitDoesNotCreateSharedRemote(t *testing.T) {
	work := setupTestRepo(t)
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t, p)
	ctx := context.Background()
	configFile := filepath.Join(work, ".git", "config")
	before, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := InitWithOptions(ctx, d, p, work, InitOptions{Isolated: true}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("isolated init created shared remote wiring")
	}
}

func TestIsolatedInitKeepsSharedWorktreeConfig(t *testing.T) {
	work := setupTestRepo(t)
	ctx := context.Background()
	foreign := filepath.Join(t.TempDir(), "active-task.git")
	if err := gitpkg.AddRemote(ctx, work, RemoteName, foreign); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(work, ".git", "config")
	before, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	var gates []string
	for _, branch := range []string{"task/first", "task/second"} {
		linked := filepath.Join(t.TempDir(), "linked")
		if _, err := gitpkg.Run(ctx, work, "worktree", "add", "-b", branch, linked, "HEAD"); err != nil {
			t.Fatal(err)
		}
		p := paths.WithRoot(t.TempDir())
		if err := p.EnsureDirs(); err != nil {
			t.Fatal(err)
		}
		d := openTestDB(t, p)
		for _, createdWant := range []bool{true, false} {
			repo, created, err := InitWithOptions(ctx, d, p, linked, InitOptions{Isolated: true})
			if err != nil {
				t.Fatal(err)
			}
			if created != createdWant || repo.WorkingPath != work {
				t.Fatalf("created=%v path=%q", created, repo.WorkingPath)
			}
			gateDir := p.RepoDir(repo.ID)
			if created {
				gates = append(gates, gateDir)
			}
			for _, hook := range []string{"pre-receive", "post-receive"} {
				if !fileExists(filepath.Join(gateDir, "hooks", hook)) {
					t.Fatalf("missing %s", hook)
				}
			}
			if got, err := gitpkg.GetRemoteURL(ctx, gateDir, "origin"); err != nil || got == "" {
				t.Fatalf("gate origin=%q: %v", got, err)
			}
		}
		if _, err := Eject(ctx, d, p, linked); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(configFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("isolated init/refresh/eject changed shared configuration")
		}
	}
	if gates[0] == gates[1] {
		t.Fatal("separate homes shared a gate")
	}
}

func TestRefreshRefusesAnotherTasksRemote(t *testing.T) {
	work := setupTestRepo(t)
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t, p)
	ctx := context.Background()
	repo, _, err := Init(ctx, d, p, work)
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(t.TempDir(), "active-task.git")
	if err := gitpkg.EnsureRemote(ctx, work, RemoteName, foreign); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Init(ctx, d, p, work); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("refresh error=%v", err)
	}
	if got, err := gitpkg.GetRemoteURL(ctx, work, RemoteName); err != nil || got != foreign {
		t.Fatalf("foreign remote changed to %q: %v", got, err)
	}
	if _, err := os.Stat(p.RepoDir(repo.ID)); err != nil {
		t.Fatalf("refresh destroyed own existing gate: %v", err)
	}
}

func TestIsolatedInitRollbackPreservesForeignRemote(t *testing.T) {
	work := setupTestRepo(t)
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t, p)
	ctx := context.Background()
	foreign := filepath.Join(t.TempDir(), "active-task.git")
	if err := gitpkg.AddRemote(ctx, work, RemoteName, foreign); err != nil {
		t.Fatal(err)
	}
	oldEnsure := ensureGateHooksPathIsolation
	ensureGateHooksPathIsolation = func(context.Context, string) (bool, error) {
		// Force registration to fail after provisioning, exercising DB rollback.
		_ = d.Close()
		return false, nil
	}
	t.Cleanup(func() { ensureGateHooksPathIsolation = oldEnsure })
	if _, _, err := InitWithOptions(ctx, d, p, work, InitOptions{Isolated: true}); err == nil || !strings.Contains(err.Error(), "insert repo") {
		t.Fatalf("init error=%v", err)
	}
	if got, err := gitpkg.GetRemoteURL(ctx, work, RemoteName); err != nil || got != foreign {
		t.Fatalf("foreign remote changed to %q: %v", got, err)
	}
	if _, err := os.Stat(p.RepoDir(repoID(work))); !os.IsNotExist(err) {
		t.Fatalf("failed gate remains: %v", err)
	}
}
