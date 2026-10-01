// Package agentgitproxy owns the author-side publication boundary. Its receipt
// records local validation; it never establishes independent MR approval.
package agentgitproxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const Protocol = "com.ater.agent-git-proxy/v1"
const ValidationGeneration = "agent-git-proxy-v1"
const MaxContextBytes = 128 * 1024

// Context is written by the accepted launcher after task admission. Missing
// intent is a closed publication gate, never an invitation for the agent to
// invent acceptance criteria from its own diff.
type Context struct {
	Protocol          string `json:"protocol"`
	Project           string `json:"project"`
	CheckoutRoot      string `json:"checkout_root"`
	IntegrationBranch string `json:"integration_branch"`
	Intent            string `json:"intent"`
	Assignment        string `json:"assignment"`
	AssignmentField   string `json:"assignment_field"`
	ClaimFile         string `json:"claim_file"`
	SessionID         string `json:"session_id"`
	UpstreamSHA256    string `json:"upstream_sha256"`
	PublisherPath     string `json:"publisher_path,omitempty"`
	PublisherSHA256   string `json:"publisher_sha256,omitempty"`
}

func readPrivateFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || runtime.GOOS == "windows" {
		return nil, fmt.Errorf("agent Git proxy requires an absolute private file")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("agent Git proxy has no admitted task context")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("agent Git proxy task context is not a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read agent Git proxy task context: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("agent Git proxy private file changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxContextBytes+1))
	if err != nil || len(b) > MaxContextBytes || !utf8.Valid(b) {
		return nil, fmt.Errorf("agent Git proxy task context exceeds its bound or cannot be read")
	}
	return b, nil
}

func LoadContext(path string) (*Context, error) {
	b, err := readPrivateFile(path)
	if err != nil {
		return nil, err
	}
	var c Context
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid agent Git proxy task context")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("agent Git proxy task context has trailing data")
	}
	if c.Protocol != Protocol || c.Project == "" || !filepath.IsAbs(c.CheckoutRoot) ||
		c.IntegrationBranch == "" || c.Assignment == "" || strings.TrimSpace(c.Intent) == "" ||
		!filepath.IsAbs(c.ClaimFile) || c.SessionID == "" || !validHex(c.UpstreamSHA256, 64) ||
		strings.ContainsRune(c.Intent, '\x00') || strings.HasPrefix(c.IntegrationBranch, "task/") ||
		(c.AssignmentField != "ticket_id" && c.AssignmentField != "xp_id") {
		return nil, fmt.Errorf("agent Git proxy requires an admitted assignment and its exact intent")
	}
	if err := validateClaimFence(&c); err != nil {
		return nil, err
	}
	if (c.PublisherPath != "" || c.PublisherSHA256 != "") &&
		(!filepath.IsAbs(c.PublisherPath) || !validHex(c.PublisherSHA256, 64)) {
		return nil, fmt.Errorf("agent Git proxy has an invalid canonical publisher binding")
	}
	return &c, nil
}

func URLDigest(url string) string {
	digest := sha256.Sum256([]byte(url))
	return hex.EncodeToString(digest[:])
}

// LaunchNonce makes retries reuse the same durable run, while a changed task,
// destination, intent or submitted commit requires fresh validation.
func LaunchNonce(c *Context, submitted, branch string) string {
	b, _ := json.Marshal(struct {
		Context   *Context `json:"context"`
		Submitted string   `json:"submitted"`
		Branch    string   `json:"branch"`
	}{c, submitted, branch})
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

func ValidateLaunch(c *Context, run *db.Run) error {
	if c == nil || run == nil || run.SubmittedHeadSHA == nil || !validObjectID(*run.SubmittedHeadSHA) ||
		run.Intent == nil || *run.Intent != c.Intent || run.PRBaseBranch == nil || *run.PRBaseBranch != c.IntegrationBranch ||
		run.LaunchNonce == nil || *run.LaunchNonce != LaunchNonce(c, *run.SubmittedHeadSHA, run.Branch) ||
		run.LaunchValidationGeneration == nil || *run.LaunchValidationGeneration != ValidationGeneration {
		return fmt.Errorf("agent Git proxy run is not bound to this admitted publication")
	}
	return nil
}

type claimProjection struct {
	Project   string          `json:"project"`
	SessionID string          `json:"session_id"`
	State     string          `json:"state"`
	TicketID  json.RawMessage `json:"ticket_id"`
	XPID      string          `json:"xp_id"`
	Held      bool            `json:"claim_held_by_session"`
	ExpiresAt string          `json:"expires_at"`
	Renewal   struct {
		State string `json:"state"`
	} `json:"renewal"`
	Claims []*claimProjection `json:"claims"`
}

// The public projection can revoke this captured assignment; it never grants
// independent review authority. It must remain current, healthy and unexpired
// before both run submission and the executor's actual remote mutation.
func validateClaimFence(c *Context) error {
	b, err := readPrivateFile(c.ClaimFile)
	var root claimProjection
	if err != nil || len(b) > MaxContextBytes || json.Unmarshal(b, &root) != nil ||
		root.Project != c.Project || root.SessionID != c.SessionID || (root.State != "" && root.State != "active") {
		return fmt.Errorf("agent Git proxy assignment is unavailable or changed")
	}
	claims := root.Claims
	if claims == nil {
		claims = []*claimProjection{&root}
	}
	if len(claims) != 1 || claims[0] == nil {
		return fmt.Errorf("agent Git proxy requires exactly its admitted assignment")
	}
	claim := claims[0]
	identity := string(claim.TicketID)
	if c.AssignmentField == "xp_id" {
		identity = claim.XPID
	}
	expires, err := time.Parse(time.RFC3339Nano, claim.ExpiresAt)
	if identity != c.Assignment || claim.Project != c.Project || !claim.Held ||
		claim.Renewal.State != "healthy" || err != nil || !time.Now().Before(expires) {
		return fmt.Errorf("agent Git proxy assignment was released, replaced or expired")
	}
	return nil
}

// ParsePush accepts one publication of the current task branch. Destructive,
// multi-ref, alternate-head and hook-bypass requests are rejected before init
// or remote mutation. A retry drives the same branch's existing run.
func ParsePush(args []string, branch string) (remote string, upstream bool, err error) {
	if !strings.HasPrefix(branch, "task/") || len(branch) == len("task/") {
		return "", false, fmt.Errorf("agent Git proxy publishes only the current task/ branch")
	}
	return ParseBranchPush(args, branch)
}

// Canonical callers additionally require exact validated task evidence.
// ParseTaskDelete accepts cleanup of exactly the caller's task branch. The
// controller additionally requires completed validation and an exact remote
// lease, so cleanup cannot delete another publisher's replacement head.
func ParseTaskDelete(args []string, branch string) (remote string, requested bool, err error) {
	var positional []string
	for _, arg := range args {
		if arg == "--delete" || arg == "-d" {
			requested = true
			continue
		}
		if strings.HasPrefix(arg, ":") {
			requested = true
		}
		positional = append(positional, arg)
	}
	if !requested {
		return "", false, nil
	}
	if !strings.HasPrefix(branch, "task/") || len(positional) != 2 || strings.HasPrefix(positional[0], "-") ||
		(positional[1] != branch && positional[1] != "refs/heads/"+branch && positional[1] != ":refs/heads/"+branch && positional[1] != ":"+branch) {
		return "", true, fmt.Errorf("agent Git proxy cleanup deletes only the current task branch")
	}
	return positional[0], true, nil
}

func ParseBranchPush(args []string, branch string) (remote string, upstream bool, err error) {
	var positional []string
	for _, arg := range args {
		switch arg {
		case "-u", "--set-upstream":
			upstream = true
		case "--quiet", "-q", "--verbose", "-v", "--porcelain":
			// Output preferences do not change the publication being requested.
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("agent Git proxy refuses unsupported push options")
			}
			positional = append(positional, arg)
		}
	}
	if len(positional) > 2 {
		return "", false, fmt.Errorf("agent Git proxy requires one current-branch publication")
	}
	remote = "origin"
	if len(positional) > 0 {
		remote = positional[0]
	}
	if len(positional) == 2 {
		ref := positional[1]
		allowed := ref == branch || ref == "HEAD" || ref == "HEAD:"+branch ||
			ref == "HEAD:refs/heads/"+branch || ref == branch+":"+branch ||
			ref == "refs/heads/"+branch+":refs/heads/"+branch
		if !allowed {
			return "", false, fmt.Errorf("agent Git proxy refuses another source or destination ref")
		}
	}
	return remote, upstream, nil
}

type Receipt struct {
	Protocol        string `json:"protocol"`
	RunID           string `json:"run_id"`
	Project         string `json:"project"`
	Assignment      string `json:"assignment"`
	SessionID       string `json:"session_id"`
	Branch          string `json:"branch"`
	SubmittedSHA    string `json:"submitted_sha"`
	PublishedSHA    string `json:"published_sha"`
	ReviewedSHA     string `json:"reviewed_sha"`
	IntentSHA256    string `json:"intent_sha256"`
	PushFingerprint string `json:"push_fingerprint"`
}

func ValidateGate(s *db.StepResult) error {
	if s == nil || s.Status != types.StepStatusCompleted || s.Error != nil || s.OverrideReason != nil {
		return fmt.Errorf("agent Git proxy requires a completed gate without an error or override")
	}
	if s.FindingsJSON != nil && strings.TrimSpace(*s.FindingsJSON) != "" {
		findings, err := types.ParseFindingsJSON(*s.FindingsJSON)
		if err != nil {
			return fmt.Errorf("agent Git proxy cannot read gate findings")
		}
		for _, f := range findings.Items {
			if f.Severity != "info" || f.ActionOrDefault() != types.ActionNoOp {
				return fmt.Errorf("agent Git proxy refuses unresolved %s findings", s.StepName)
			}
		}
	}
	return nil
}

// ValidateReceipt rejects successful command exits without complete durable
// gate and publication evidence. Skipped local Test/PR/CI are intentional and
// supply no external CI evidence.
func ValidateReceipt(c *Context, run *db.Run, steps []*db.StepResult, submitted, branch string) (*Receipt, error) {
	if err := ValidateLaunch(c, run); err != nil {
		return nil, err
	}
	if c == nil || run == nil || run.Status != types.RunCompleted || run.Branch != branch ||
		!strings.HasPrefix(branch, "task/") || !validObjectID(submitted) || !validObjectID(run.HeadSHA) ||
		run.ID == "" || filepath.Base(run.ID) != run.ID || strings.ContainsAny(run.ID, "\\\x00") ||
		run.PRBaseBranch == nil || *run.PRBaseBranch != c.IntegrationBranch ||
		run.Intent == nil || *run.Intent != c.Intent || run.SubmittedHeadSHA == nil ||
		!validObjectID(*run.SubmittedHeadSHA) ||
		(*run.SubmittedHeadSHA != submitted && run.HeadSHA != submitted) ||
		run.ReviewApprovedHeadSHA == nil || !validObjectID(*run.ReviewApprovedHeadSHA) ||
		run.LastPushedSHA == nil || *run.LastPushedSHA != run.HeadSHA ||
		run.PushTargetFingerprint == nil || *run.PushTargetFingerprint == "" || run.PushRef == nil || *run.PushRef != "refs/heads/"+branch {
		return nil, fmt.Errorf("agent Git proxy has no completed validation bound to this publication")
	}
	seen := make(map[types.StepName]bool, len(steps))
	for _, s := range steps {
		if s == nil || s.RunID != run.ID || seen[s.StepName] || s.Error != nil || s.OverrideReason != nil {
			return nil, fmt.Errorf("agent Git proxy validation evidence is incomplete or overridden")
		}
		seen[s.StepName] = true
		want := types.StepStatusCompleted
		if s.StepName == types.StepTest || s.StepName == types.StepPR || s.StepName == types.StepCI {
			want = types.StepStatusSkipped
		}
		if s.Status != want {
			return nil, fmt.Errorf("agent Git proxy requires completed %s evidence", s.StepName)
		}
		if want == types.StepStatusCompleted {
			if err := ValidateGate(s); err != nil {
				return nil, err
			}
		}
	}
	for _, name := range []types.StepName{types.StepIntent, types.StepRebase, types.StepReview, types.StepTest,
		types.StepDocument, types.StepLint, types.StepPush, types.StepPR, types.StepCI} {
		if !seen[name] {
			return nil, fmt.Errorf("agent Git proxy is missing %s evidence", name)
		}
	}
	digest := sha256.Sum256([]byte(c.Intent))
	return &Receipt{Protocol: Protocol, RunID: run.ID, Project: c.Project, Branch: branch,
		Assignment: c.Assignment, SessionID: c.SessionID,
		SubmittedSHA: *run.SubmittedHeadSHA, PublishedSHA: run.HeadSHA, ReviewedSHA: *run.ReviewApprovedHeadSHA,
		IntentSHA256: hex.EncodeToString(digest[:]), PushFingerprint: *run.PushTargetFingerprint}, nil
}

func validHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validObjectID(value string) bool {
	return validHex(value, 40) || validHex(value, 64)
}
