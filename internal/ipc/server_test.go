package ipc_test

import (
	"os"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
)

// socketPath returns a short socket path to stay within macOS 104-byte limit.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ipc")
	if err != nil {
		t.Fatal(err)
	}
	p := paths.WithRoot(dir)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(p.Socket()); _ = os.RemoveAll(dir) })
	return p.Socket()
}

func startServer(t *testing.T, sock string) *ipc.Server {
	t.Helper()
	srv := ipc.NewServer()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(sock) }()

	// wait for server to be ready
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := ipc.Dial(sock)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		srv.Close()
		if err := <-errCh; err != nil {
			// server returns nil on clean close
		}
	})
	return srv
}
