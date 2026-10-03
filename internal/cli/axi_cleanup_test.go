package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
)

func TestCleanupRequiresExplicitRun(t *testing.T) {
	cmd := newAxiCleanupCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("cleanup accepted an implicit run")
	}
	if !strings.Contains(output.String(), "cleanup requires --run <id>") {
		t.Fatalf("missing actionable error: %s", output.String())
	}
}

func TestStatusExposesRecordedRecoveryWorktree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-root", "run-id")
	fromDB := runViewFromDB(&db.Run{ID: "run-id", WorktreeDir: &path}, nil, nil)
	fromIPC := runViewFromIPC(&ipc.RunInfo{ID: "run-id", WorktreeDir: path})
	for _, view := range []runView{fromDB, fromIPC} {
		if view.Worktree != path {
			t.Fatalf("recovery worktree not exposed: %+v", view)
		}
	}
}
