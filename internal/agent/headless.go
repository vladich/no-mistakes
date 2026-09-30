package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/kunchenguid/no-mistakes/internal/shellenv"
)

// headlessAgent drives the text-only one-shot interfaces of Deep Code and
// Qwen Code. Deep Code 0.3.1 has no structured-output flag. Both receive the
// complete duty and output schema on stdin and are validated by no-mistakes.
// A failed turn is never replayed: either CLI may already have executed tools.
type headlessAgent struct {
	name      string
	bin       string
	extraArgs []string
	subprocessContext
}

func (a *headlessAgent) Name() string               { return a.name }
func (a *headlessAgent) ReportsAgentAttempts() bool { return true }
func (a *headlessAgent) Close() error               { return nil }

const headlessOutputLimit = 8 << 20
const headlessStdinInstruction = "Follow the complete task instructions supplied on standard input."

func (a *headlessAgent) buildArgs() []string {
	args := append([]string(nil), a.extraArgs...)
	if a.name == "deepcode" {
		return append(args, "--exec", "--prompt", headlessStdinInstruction)
	}
	if !headlessPermissionPinned(a.extraArgs) {
		args = append(args, "--yolo")
	}
	return append(args, "--output-format", "text", "--prompt", headlessStdinInstruction)
}

func headlessPermissionPinned(args []string) bool {
	for _, arg := range args {
		for _, flag := range []string{"--yolo", "-y", "--approval-mode"} {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return true
			}
		}
	}
	return false
}

func (a *headlessAgent) Run(ctx context.Context, opts RunOpts) (_ *Result, retErr error) {
	prompt := opts.Prompt
	if len(opts.JSONSchema) > 0 {
		prompt = buildACPStructuredPrompt(prompt, opts.JSONSchema)
	}
	cmd := exec.CommandContext(ctx, a.bin, a.buildArgs()...)
	cmd.Dir = opts.CWD
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = a.gitSafeEnv(opts.CWD, opts.Env)
	shellenv.ConfigureShellCommand(cmd)
	started, err := startNativeAgentCommand(cmd, nativeAgentActivityObserver(opts, a.name))
	if err != nil {
		return nil, fmt.Errorf("%s start: %w", a.name, err)
	}
	defer started.closePipes()
	emitAgentStarted(opts, a.name, started.pid())
	defer func() { emitAgentExited(opts, a.name, started.pid(), retErr) }()

	var stderr []byte
	var stderrWG sync.WaitGroup
	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		stderr, _ = io.ReadAll(io.LimitReader(started.stderr, 8192))
		// Drain excess diagnostics without retaining them or blocking the child.
		_, _ = io.Copy(io.Discard, started.stderr)
	}()
	text, readErr := readHeadlessOutput(started.stdout, opts.OnChunk)
	if readErr != nil {
		err = started.waitAfterParseError(readErr)
	} else {
		err = started.wait()
	}
	stderrWG.Wait()
	if err != nil {
		return nil, fmt.Errorf("%s headless run: %w: %s", a.name, err, strings.TrimSpace(string(stderr)))
	}
	return finalizeTextResult(a.name, text, opts.JSONSchema, TokenUsage{})
}

func readHeadlessOutput(reader io.Reader, onChunk func(string)) (string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), headlessOutputLimit)
	var output strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if output.Len()+len(line)+1 > headlessOutputLimit {
			return "", errors.New("headless output exceeds size limit")
		}
		output.WriteString(line)
		output.WriteByte('\n')
		if onChunk != nil {
			onChunk(line)
		}
	}
	return strings.TrimSpace(output.String()), scanner.Err()
}
