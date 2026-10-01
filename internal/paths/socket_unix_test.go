//go:build !windows

package paths

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLongHomeHasPrivateBindableSocket(t *testing.T) {
	root := filepath.Join(t.TempDir(), strings.Repeat("deep", 40))
	p := WithRoot(root)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(p.Socket()) })
	if len(p.Socket()) > 103 || p.Socket() == WithRoot(root+"other").Socket() {
		t.Fatalf("unusable or aliased IPC endpoint: %q", p.Socket())
	}
	if p.DB() != filepath.Join(root, "state.sqlite") || p.WorktreesDir() != filepath.Join(root, "worktrees") {
		t.Fatal("long home moved durable state")
	}
	listener, err := net.Listen("unix", p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := net.Dial("unix", p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	info, err := os.Lstat(filepath.Dir(p.Socket()))
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("private IPC directory: %v, %v", info, err)
	}
}

func TestPrivateSocketDirectoryRefusesSymlinkAndSharedPermissions(t *testing.T) {
	for _, kind := range []string{"symlink", "shared"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "ipc")
			if kind == "symlink" {
				if err := os.Symlink(root, dir); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := ensurePrivateSocketDir(dir); err == nil {
				t.Fatal("unsafe IPC directory admitted")
			}
		})
	}
}
