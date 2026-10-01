//go:build !windows

package paths

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Darwin's sockaddr_un has room for 103 path bytes plus the terminator.
// Task homes can exceed that limit. Only the IPC endpoint moves: Git objects,
// CoW worktrees, configuration and evidence remain under the original home.
func socketPath(root string) string {
	endpoint := filepath.Join(root, "socket")
	if len(endpoint) <= 103 {
		return endpoint
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		absolute = filepath.Clean(root)
	}
	digest := sha256.Sum256([]byte(absolute))
	return filepath.Join(privateSocketDir(), fmt.Sprintf("%x.sock", digest))
}

func privateSocketDir() string {
	// TMPDIR may itself be the long task path. /tmp is the Unix runtime
	// namespace; a checked owner-only directory prevents other users from
	// substituting an endpoint or learning the launcher context through IPC.
	return filepath.Join("/tmp", fmt.Sprintf("no-mistakes-ipc-%d", os.Getuid()))
}

func ensureSocketDir(root string) error {
	endpoint := socketPath(root)
	if endpoint == filepath.Join(root, "socket") {
		return nil
	}
	return ensurePrivateSocketDir(filepath.Dir(endpoint))
}

func ensurePrivateSocketDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create private IPC directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect private IPC directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("IPC directory %q must be a real owner-only directory owned by this user", dir)
	}
	return nil
}
