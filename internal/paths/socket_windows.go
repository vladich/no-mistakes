package paths

import "path/filepath"

// Windows IPC uses a TCP endpoint descriptor, without a Unix path limit.
func socketPath(root string) string { return filepath.Join(root, "socket") }
func ensureSocketDir(string) error  { return nil }
