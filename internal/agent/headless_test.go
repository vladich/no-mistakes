package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestHeadlessAgentsRunWithStdinSchemaAndInheritedEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	for _, name := range []types.AgentName{types.AgentDeepCode, types.AgentQwen} {
		t.Run(string(name), func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, string(name))
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > argv.txt\ncat > stdin.txt\nprintf '%s' \"$GATE_CANARY\" > env.txt\nprintf '%s\\n' '{\"ok\":true}'\n"
			if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			a, err := NewWithOptions(name, bin, nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			prompt := strings.Repeat("large-private-prompt ", 10000)
			result, err := a.Run(context.Background(), RunOpts{
				CWD: dir, Prompt: prompt, Env: []string{"GATE_CANARY=inherited"},
				JSONSchema: []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`),
			})
			if err != nil || string(result.Output) != `{"ok":true}` {
				t.Fatalf("result=%+v, error=%v", result, err)
			}
			argv, _ := os.ReadFile(filepath.Join(dir, "argv.txt"))
			stdin, _ := os.ReadFile(filepath.Join(dir, "stdin.txt"))
			env, _ := os.ReadFile(filepath.Join(dir, "env.txt"))
			if strings.Contains(string(argv), "large-private-prompt") || !strings.Contains(string(stdin), prompt) || !strings.Contains(string(stdin), "final output contract") || string(env) != "inherited" {
				t.Fatal("stdin prompt/schema or inherited environment was lost")
			}
			if name == types.AgentDeepCode && !strings.Contains(string(argv), "--exec") {
				t.Fatal("Deep Code would enter its interactive UI")
			}
			if name == types.AgentQwen && !strings.Contains(string(argv), "--yolo") {
				t.Fatal("Qwen Code would wait for tool confirmation")
			}
		})
	}
}

func TestHeadlessFailureNeverReplaysTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "deepcode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat >/dev/null\necho tool >> effects.txt\necho '429 rate limit' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a, _ := New(types.AgentDeepCode, bin, nil)
	if _, err := a.Run(context.Background(), RunOpts{CWD: dir, Prompt: "task"}); err == nil {
		t.Fatal("failed CLI passed")
	}
	effects, _ := os.ReadFile(filepath.Join(dir, "effects.txt"))
	if string(effects) != "tool\n" {
		t.Fatalf("failed turn replayed its tools: %q", effects)
	}
}

func TestHeadlessOutputRejectsOversizedResponse(t *testing.T) {
	if _, err := readHeadlessOutput(strings.NewReader(strings.Repeat("x\n", headlessOutputLimit)), nil); err == nil {
		t.Fatal("oversized response passed")
	}
}

func TestHeadlessPermissionOverrideIsPreserved(t *testing.T) {
	a := headlessAgent{name: "qwen", extraArgs: []string{"--approval-mode=auto-edit"}}
	if strings.Contains(strings.Join(a.buildArgs(), " "), "--yolo") {
		t.Fatal("operator permission override was replaced")
	}
}

func TestHeadlessModelPinsAndInstructionSuppressionFailClosed(t *testing.T) {
	a, err := NewWithOptions(types.AgentQwen, "qwen", nil, Options{Profile: agentcfg.Profile{Model: "selected-model"}})
	if err != nil || !strings.Contains(strings.Join(a.(*headlessAgent).buildArgs(), " "), "--model selected-model") {
		t.Fatalf("Qwen model pin was lost: %v", err)
	}
	for _, name := range []types.AgentName{types.AgentQwen, types.AgentDeepCode} {
		a, err := NewWithOptions(name, "bin", nil, Options{DisableProjectSettings: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := EnsureGateNeutralized(a); err == nil {
			t.Fatal("unverified instruction suppression was admitted")
		}
	}
}
