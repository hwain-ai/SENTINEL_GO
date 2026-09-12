package gomutesting

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hwain-hwang/sentinel-go/internal/mutation"
	"github.com/hwain-hwang/sentinel-go/internal/runner"
)

func TestReplayRejectsMissingProofEvenWithMatchingStatusAndCount(t *testing.T) {
	first := runner.Report{SchemaVersion: runner.RunnerSchema, Nonce: strings.Repeat("a", 32),
		Status: runner.StatusAssertionFailure, InventoryCount: 1}
	second := first
	second.Nonce = strings.Repeat("b", 32)
	if classify(first, second) != mutation.ToolError {
		t.Fatal("matching counts and failure strings are not replay proof")
	}
}

func TestSameTestAlternatingAssertionSitesNeverCountsAsKilled(t *testing.T) {
	request := projectFixture(t, positiveSource, "")
	counter := filepath.Join(t.TempDir(), "counter")
	source := fmt.Sprintf(`package probe
import ("os"; "testing")
func TestPositive(t *testing.T) {
  if !Positive(1) || Positive(0) || Positive(-1) {
    old, _ := os.ReadFile(%q)
    if err := os.WriteFile(%q, append(old, 'x'), 0600); err != nil { panic(err) }
    if len(old) %% 2 == 0 { t.Error("first assertion") } else { t.Error("different assertion") }
  }
}
`, counter, counter)
	if err := os.WriteFile(filepath.Join(request.ProjectRoot, "value_test.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), request)
	if err != nil || report.Counts[mutation.Killed] != 0 || report.Counts[mutation.ToolError] != 3 {
		t.Fatalf("alternating failure accepted: counts=%v err=%v", report.Counts, err)
	}
	for _, replay := range report.Replays {
		if replay.First.Status != runner.StatusAssertionFailure || replay.Second.Status != runner.StatusAssertionFailure {
			t.Fatal("fixture did not reproduce two assertion failures")
		}
		if replay.First.FailureSHA256 == replay.Second.FailureSHA256 {
			t.Fatal("different sites have the same identity")
		}
	}
}

func TestSuccessfulProbeBindsControlsPlanAndEveryReplay(t *testing.T) {
	request := projectFixture(t, positiveSource, `if !Positive(1) || Positive(0) || Positive(-1) { t.Fatal("wrong") }`)
	report, err := Run(context.Background(), request)
	if err != nil || !validHex(report.ProjectSHA256, 32) || !validHex(report.PlanSHA256, 32) {
		t.Fatalf("missing input identity: %#v %v", report, err)
	}
	if len(report.Replays) != len(report.Candidates) || len(report.Replays) != 3 {
		t.Fatal("missing candidate replay identities")
	}
	nonces := map[string]bool{}
	for _, control := range report.Control {
		if control.InputSHA256 != report.ProjectSHA256 || control.Status != runner.StatusPassed || nonces[control.Nonce] {
			t.Fatal("control binding mismatch")
		}
		nonces[control.Nonce] = true
	}
	for index, replay := range report.Replays {
		if replay.CandidateID != report.Candidates[index].ID || replay.First.InputSHA256 == report.ProjectSHA256 {
			t.Fatal("mutant was not bound to its changed source")
		}
		for _, execution := range []ExecutionIdentity{replay.First, replay.Second} {
			if execution.InventorySHA256 != report.Control[0].InventorySHA256 || nonces[execution.Nonce] || !validHex(execution.FailureSHA256, 32) {
				t.Fatal("reused execution or different test inventory")
			}
			nonces[execution.Nonce] = true
		}
		if replay.First.InputSHA256 != replay.Second.InputSHA256 || replay.First.FailureSHA256 != replay.Second.FailureSHA256 {
			t.Fatal("replay changed source or failure identity")
		}
	}
	original := report.PlanSHA256
	report.ProjectSHA256 = strings.Repeat("0", 64)
	if planDigest(report) == original {
		t.Fatal("plan is not source bound")
	}
}

func TestPlanDigestBindsZeroCandidateSourceScope(t *testing.T) {
	first := newReport()
	first.ProjectSHA256 = strings.Repeat("a", 64)
	first.Sources = []mutation.SourceInventory{{Path: "first.go", SHA256: strings.Repeat("b", 64), CandidateCount: 0}}
	second := first
	second.Sources = []mutation.SourceInventory{{Path: "second.go", SHA256: strings.Repeat("b", 64), CandidateCount: 0}}
	if planDigest(first) == planDigest(second) {
		t.Fatal("two explicit source scopes with no candidates have the same plan identity")
	}
}

func TestTestSideTestSourceChangesCannotBeHiddenByRestoration(t *testing.T) {
	request := projectFixture(t, positiveSource, "")
	source := "package probe\nimport (\"os\"; \"testing\")\nfunc TestPositive(t *testing.T) { if err := os.WriteFile(\"value_test.go\", []byte(\"package probe\\n\"), 0600); err != nil { panic(err) } }\n"
	path := filepath.Join(request.ProjectRoot, "value_test.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), request)
	if err == nil || len(report.Records) != 0 {
		t.Fatalf("test source tampering accepted: %#v %v", report, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != source {
		t.Fatal("original project test source changed")
	}
}

func proofReport(status runner.Status, nonce string) runner.Report {
	report := runner.Report{SchemaVersion: runner.RunnerSchema, Nonce: strings.Repeat(nonce, 32), Status: status,
		InventoryCount: 1, InputSHA256: strings.Repeat("c", 64), Replay: &runner.ReplayIdentity{
			InventorySHA256: strings.Repeat("d", 64), ResultsSHA256: strings.Repeat("e", 64),
			FailureSHA256: strings.Repeat("f", 64)}}
	if status != runner.StatusAssertionFailure {
		report.Replay.FailureSHA256 = ""
	}
	return report
}

func TestReplayRejectsDifferentInventoryResultsFailureSitesAndSource(t *testing.T) {
	first := proofReport(runner.StatusAssertionFailure, "a")
	for _, field := range []string{"inventory", "results", "failure", "source", "missing-failure", "bad-nonce"} {
		first := first
		identity := *first.Replay
		first.Replay = &identity
		second := proofReport(runner.StatusAssertionFailure, "b")
		switch field {
		case "inventory":
			second.Replay.InventorySHA256 = strings.Repeat("0", 64)
		case "results":
			second.Replay.ResultsSHA256 = strings.Repeat("0", 64)
		case "failure":
			second.Replay.FailureSHA256 = strings.Repeat("0", 64)
		case "source":
			second.InputSHA256 = strings.Repeat("0", 64)
		case "missing-failure":
			first.Replay.FailureSHA256 = ""
			second.Replay.FailureSHA256 = ""
		case "bad-nonce":
			second.Nonce = strings.Repeat("z", 32)
		}
		if classify(first, second) != mutation.ToolError {
			t.Fatalf("accepted %s mismatch", field)
		}
	}
}
