package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentGitProxyIsGlobalOnly(t *testing.T) {
	gitPath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "context.json")
	data := []byte(fmt.Sprintf("agent_git_proxy:\n  git_binary: %q\n  context_file: %q\n", gitPath, path))
	global, err := LoadGlobalFromBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if global.AgentGitProxy == nil || global.AgentGitProxy.GitBinary != gitPath {
		t.Fatal("global proxy config lost")
	}
	repo, err := LoadRepoFromBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := Merge(DefaultGlobalConfig(), repo).AgentGitProxy; got != nil {
		t.Fatal("repository enabled proxy mode")
	}
	if got := Merge(global, repo).AgentGitProxy; got == nil || got.ContextFile != path {
		t.Fatal("global proxy configuration lost in merge")
	}
	if _, err := LoadGlobalFromBytes([]byte("agent_git_proxy:\n  git_binary: git\n  context_file: context.json\n")); err == nil {
		t.Fatal("relative proxy paths admitted")
	}
}
