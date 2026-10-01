package agentgitproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestParsePush(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		ok       bool
		upstream bool
	}{
		{"default", nil, true, false},
		{"upstream", []string{"-u", "origin", "task/test"}, true, true},
		{"head", []string{"origin", "HEAD"}, true, false},
		{"explicit", []string{"origin", "HEAD:refs/heads/task/test"}, true, false},
		{"other-destination", []string{"origin", "HEAD:dev"}, false, false},
		{"other-source", []string{"origin", "task/other:task/test"}, false, false},
		{"delete", []string{"origin", ":task/test"}, false, false},
		{"multiple", []string{"origin", "task/test", "task/other"}, false, false},
		{"all", []string{"--all"}, false, false},
		{"mirror", []string{"--mirror"}, false, false},
		{"force", []string{"--force"}, false, false},
		{"hook-bypass", []string{"--no-verify"}, false, false},
		{"skip-option", []string{"-o", "ci.skip"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote, upstream, err := ParsePush(tc.args, "task/test")
			if (err == nil) != tc.ok || (tc.ok && (remote != "origin" || upstream != tc.upstream)) {
				t.Fatalf("result: remote=%q upstream=%v error=%v", remote, upstream, err)
			}
		})
	}
	if _, _, err := ParsePush(nil, "dev"); err == nil {
		t.Fatal("canonical branch was admitted as a task publication")
	}
}

func validContext(t *testing.T) *Context {
	root := t.TempDir()
	claimFile := filepath.Join(root, "claim.json")
	claim := map[string]any{"project": "test", "session_id": "session", "ticket_id": 42, "claim_held_by_session": true, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), "renewal": map[string]any{"state": "healthy"}}
	b, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claimFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return &Context{Protocol: Protocol, Project: "test", CheckoutRoot: root, IntegrationBranch: "dev", Intent: "preserve the user's goal", Assignment: "42", AssignmentField: "ticket_id", ClaimFile: claimFile, SessionID: "session", UpstreamSHA256: strings.Repeat("d", 64)}
}

func TestLoadContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent Git proxy requires the accepted macOS/Linux private-file boundary")
	}
	path := filepath.Join(t.TempDir(), "context.json")
	b, _ := json.Marshal(validContext(t))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadContext(path); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"{}", string(b) + "{}", string(b[:len(b)-1]) + `,"unknown":true}`, string(make([]byte, MaxContextBytes+1))} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadContext(path); err == nil {
			t.Fatal("invalid context was admitted")
		}
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadContext(path); err == nil {
		t.Fatal("public context was admitted")
	}
	link := filepath.Join(filepath.Dir(path), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadContext(link); err == nil {
		t.Fatal("symlink context was admitted")
	}
}

func receiptFixture(t *testing.T) (*Context, *db.Run, []*db.StepResult) {
	c := validContext(t)
	submitted, head, reviewed, fingerprint, ref := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), "fingerprint", "refs/heads/task/test"
	run := &db.Run{ID: "run", Branch: "task/test", Status: types.RunCompleted, HeadSHA: head,
		PRBaseBranch:     &c.IntegrationBranch,
		SubmittedHeadSHA: &submitted, LastPushedSHA: &head, ReviewApprovedHeadSHA: &reviewed,
		PushTargetFingerprint: &fingerprint, PushRef: &ref, Intent: &c.Intent}
	nonce, generation := LaunchNonce(c, submitted, run.Branch), ValidationGeneration
	run.LaunchNonce, run.LaunchValidationGeneration = &nonce, &generation
	var steps []*db.StepResult
	for _, name := range []types.StepName{types.StepIntent, types.StepRebase, types.StepReview, types.StepTest, types.StepDocument, types.StepLint, types.StepPush, types.StepPR, types.StepCI} {
		status := types.StepStatusCompleted
		if name == types.StepTest || name == types.StepPR || name == types.StepCI {
			status = types.StepStatusSkipped
		}
		steps = append(steps, &db.StepResult{RunID: run.ID, StepName: name, Status: status})
	}
	return c, run, steps
}

func TestReceiptRequiresDurablePublicationAndEveryGate(t *testing.T) {
	c, run, steps := receiptFixture(t)
	r, err := ValidateReceipt(c, run, steps, strings.Repeat("a", 40), "task/test")
	if err != nil || r.PublishedSHA != strings.Repeat("b", 40) || r.IntentSHA256 == "" {
		t.Fatalf("receipt: %+v %v", r, err)
	}
	for _, change := range []func(*db.Run, []*db.StepResult){
		func(r *db.Run, _ []*db.StepResult) { r.LaunchNonce = nil },
		func(r *db.Run, _ []*db.StepResult) { r.LaunchValidationGeneration = nil },
		func(r *db.Run, _ []*db.StepResult) { r.ID = "../elsewhere" },
		func(r *db.Run, _ []*db.StepResult) { r.HeadSHA = "not-a-commit" },
		func(r *db.Run, _ []*db.StepResult) { r.PRBaseBranch = nil },
		func(r *db.Run, _ []*db.StepResult) { r.Status = types.RunRunning },
		func(r *db.Run, _ []*db.StepResult) { r.LastPushedSHA = nil },
		func(r *db.Run, _ []*db.StepResult) { r.ReviewApprovedHeadSHA = nil },
		func(r *db.Run, _ []*db.StepResult) { r.PushRef = nil },
		func(r *db.Run, _ []*db.StepResult) { v := "different"; r.Intent = &v },
		func(_ *db.Run, s []*db.StepResult) { s[2].Status = types.StepStatusSkipped },
		func(_ *db.Run, s []*db.StepResult) { s[3].Status = types.StepStatusCompleted },
		func(_ *db.Run, s []*db.StepResult) { v := "override"; s[2].OverrideReason = &v },
		func(_ *db.Run, s []*db.StepResult) {
			v := `{"items":[{"severity":"warning","description":"unresolved","action":"ask-user"}]}`
			s[2].FindingsJSON = &v
		},
		func(_ *db.Run, s []*db.StepResult) { v := "unreadable"; s[2].FindingsJSON = &v },
	} {
		c, run, steps := receiptFixture(t)
		change(run, steps)
		if _, err := ValidateReceipt(c, run, steps, strings.Repeat("a", 40), "task/test"); err == nil {
			t.Fatal("incomplete or unresolved validation was admitted")
		}
	}
	if _, err := ValidateReceipt(c, run, steps[:8], strings.Repeat("a", 40), "task/test"); err == nil {
		t.Fatal("missing CI skip evidence admitted")
	}
	if _, err := ValidateReceipt(c, run, steps, "different", "task/test"); err == nil {
		t.Fatal("another submission admitted")
	}
}

func TestLoadContextRejectsRevokedOrChangedAssignment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private-file launch boundary is macOS/Linux only")
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"released", func(c map[string]any) { c["claim_held_by_session"] = false }},
		{"different-ticket", func(c map[string]any) { c["ticket_id"] = 43 }},
		{"different-session", func(c map[string]any) { c["session_id"] = "another" }},
		{"different-project", func(c map[string]any) { c["project"] = "another" }},
		{"expired", func(c map[string]any) { c["expires_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano) }},
		{"renewal-uncertain", func(c map[string]any) { c["renewal"] = map[string]any{"state": "retrying"} }},
		{"closed", func(c map[string]any) { c["state"] = "closed" }},
		{"no-claim", func(c map[string]any) { c["claims"] = []any{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validContext(t)
			b, err := os.ReadFile(c.ClaimFile)
			if err != nil {
				t.Fatal(err)
			}
			var claim map[string]any
			if err := json.Unmarshal(b, &claim); err != nil {
				t.Fatal(err)
			}
			tc.change(claim)
			b, err = json.Marshal(claim)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(c.ClaimFile, b, 0o600); err != nil {
				t.Fatal(err)
			}
			b, err = json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "context.json")
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadContext(path); err == nil {
				t.Fatal("revoked or changed assignment admitted")
			}
		})
	}
}

func TestLaunchBindingRejectsTaskChangesAndReusesExactRetry(t *testing.T) {
	c, run, _ := receiptFixture(t)
	if err := ValidateLaunch(c, run); err != nil {
		t.Fatal(err)
	}
	if nonce := LaunchNonce(c, *run.SubmittedHeadSHA, run.Branch); nonce != *run.LaunchNonce {
		t.Fatal("same publication did not reuse its launch identity")
	}
	c.Assignment = "43"
	if err := ValidateLaunch(c, run); err == nil {
		t.Fatal("same intent on another task reused validation")
	}
}
