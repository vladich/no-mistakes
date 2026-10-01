package config

import (
	"fmt"
	"path/filepath"
)

// AgentGitProxyConfig is supplied by the session launcher in its private
// global configuration. It is deliberately absent from RepoConfig: the branch
// being validated cannot select its publisher or weaken the required gates.
type AgentGitProxyConfig struct {
	GitBinary   string `yaml:"git_binary"`
	ContextFile string `yaml:"context_file"`
}

func (c *AgentGitProxyConfig) Validate() error {
	if c == nil {
		return nil
	}
	if !filepath.IsAbs(c.GitBinary) || !filepath.IsAbs(c.ContextFile) {
		return fmt.Errorf("agent_git_proxy requires absolute git_binary and context_file paths")
	}
	return nil
}
