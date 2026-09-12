package gomutesting

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/mutation"
	"github.com/hwain-hwang/sentinel-go/internal/runner"
)

func TestMutantTimeoutCompletesReportAfterHangingMutant(t *testing.T) {
	source := "package probe\n\nfunc Positive(value float64) bool { return value > 0.5 }\n"
	testSource := `package probe
import "testing"
func TestPositive(t *testing.T) {
	if Positive(0) { for {} }
	if Positive(0.5) || !Positive(1) { t.Fatal("wrong") }
}
`
	request := projectFixture(t, source, "")
	testPath := filepath.Join(request.ProjectRoot, "value_test.go")
	if err := os.WriteFile(testPath, []byte(testSource), 0o600); err != nil {
		t.Fatal(err)
	}
	request.MutantTimeout = 2 * time.Second

	report, err := Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Records) != 3 || len(report.Replays) != 3 || report.Counts[mutation.TimedOut] != 1 || report.Counts[mutation.Killed] != 2 {
		t.Fatalf("records=%v counts=%v replays=%v", report.Records, report.Counts, report.Replays)
	}
	if report.Records[0].Status != mutation.Killed || report.Records[1].Status != mutation.TimedOut || report.Records[2].Status != mutation.Killed {
		t.Fatalf("later candidates did not complete after timeout: %v", report.Records)
	}
	timedOut := report.Replays[1]
	if timedOut.First.Status != runner.StatusTimedOut || timedOut.Second.Status != runner.StatusTimedOut ||
		timedOut.First.Nonce == timedOut.Second.Nonce || timedOut.First.InventorySHA256 != report.Control[0].InventorySHA256 ||
		timedOut.Second.InventorySHA256 != report.Control[0].InventorySHA256 || timedOut.First.FailureSHA256 != "" || timedOut.Second.FailureSHA256 != "" {
		t.Fatalf("timed-out replay evidence=%#v control=%#v", timedOut, report.Control)
	}
	if report.Certified {
		t.Fatal("timed-out mutant produced a certified report")
	}
	afterSource, sourceErr := os.ReadFile(filepath.Join(request.ProjectRoot, "value.go"))
	afterTest, testErr := os.ReadFile(testPath)
	if sourceErr != nil || testErr != nil || !bytes.Equal(afterSource, []byte(source)) || !bytes.Equal(afterTest, []byte(testSource)) {
		t.Fatalf("fixture bytes changed: sourceErr=%v testErr=%v", sourceErr, testErr)
	}
}

func TestControlsDoNotInheritShortMutantTimeout(t *testing.T) {
	request := projectFixture(t, "package probe\ntype Value struct{}\n", "time.Sleep(150 * time.Millisecond)")
	testPath := filepath.Join(request.ProjectRoot, "value_test.go")
	testSource := "package probe\nimport (\"testing\"; \"time\")\nfunc TestPositive(t *testing.T) { time.Sleep(150 * time.Millisecond) }\n"
	if err := os.WriteFile(testPath, []byte(testSource), 0o600); err != nil {
		t.Fatal(err)
	}
	request.MutantTimeout = 20 * time.Millisecond

	report, err := Run(context.Background(), request)
	if err != nil || len(report.Candidates) != 0 || len(report.Records) != 0 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	for _, control := range report.Control {
		if control.Status != runner.StatusPassed {
			t.Fatalf("control inherited mutant timeout: %#v", report.Control)
		}
	}
}

func TestOverallTimeoutWithMutantBudgetDiscardsReportBeforeLaterCandidate(t *testing.T) {
	request, source, testSource, hanging, later := deadlineProjectFixture(t)
	request.Timeout = 5 * time.Second
	request.MutantTimeout = 4 * time.Second

	report, err := Run(context.Background(), request)
	if err == nil || err.Error() != "goMutestingTimedOut" || len(report.Records) != 0 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	assertDeadlineFixtureState(t, request, source, testSource, hanging, later)
}

func TestCancellationWithMutantBudgetDiscardsReportBeforeLaterCandidate(t *testing.T) {
	request, source, testSource, hanging, later := deadlineProjectFixture(t)
	request.MutantTimeout = 10 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	witnessed := make(chan bool, 1)
	go func() {
		witnessed <- waitForFile(hanging, 10*time.Second)
		cancel()
	}()

	report, err := Run(ctx, request)
	if !<-witnessed {
		t.Fatal("hanging mutant did not start before cancellation")
	}
	if err == nil || err.Error() != "goMutestingCancelled" || len(report.Records) != 0 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	assertDeadlineFixtureState(t, request, source, testSource, hanging, later)
}

func TestFinalizeRunDiscardsReportAndKeepsCleanupErrorPriority(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := newReport()
	var returnedError error
	finalizeRun(ctx, &report, &returnedError)
	if returnedError == nil || returnedError.Error() != "goMutestingCancelled" || len(report.Counts) != 0 {
		t.Fatalf("late cancellation report=%#v err=%v", report, returnedError)
	}

	cleanupError := errors.New("cleanup failed")
	report = newReport()
	returnedError = cleanupError
	finalizeRun(ctx, &report, &returnedError)
	if !errors.Is(returnedError, cleanupError) || len(report.Counts) != 0 {
		t.Fatalf("cleanup priority report=%#v err=%v", report, returnedError)
	}
}

func deadlineProjectFixture(t *testing.T) (Request, string, string, string, string) {
	t.Helper()
	source := "package probe\n\nfunc Positive(value float64) bool { return value > 0.5 }\n"
	markers := t.TempDir()
	hanging := filepath.Join(markers, "hanging")
	later := filepath.Join(markers, "later")
	testSource := fmt.Sprintf(`package probe
import ("os"; "testing")
func TestPositive(t *testing.T) {
	if Positive(0) {
		if err := os.WriteFile(%q, []byte("started"), 0600); err != nil { t.Fatal(err) }
		for {}
	}
	if Positive(0.5) { t.Fatal("comparison mutant") }
	if !Positive(1) {
		if err := os.WriteFile(%q, []byte("started"), 0600); err != nil { t.Fatal(err) }
		t.Fatal("later mutant")
	}
}
`, hanging, later)
	request := projectFixture(t, source, "")
	if err := os.WriteFile(filepath.Join(request.ProjectRoot, "value_test.go"), []byte(testSource), 0o600); err != nil {
		t.Fatal(err)
	}
	return request, source, testSource, hanging, later
}

func assertDeadlineFixtureState(t *testing.T, request Request, source, testSource, hanging, later string) {
	t.Helper()
	if _, err := os.Stat(hanging); err != nil {
		t.Fatalf("hanging mutant was not exercised: %v", err)
	}
	if _, err := os.Stat(later); !os.IsNotExist(err) {
		t.Fatalf("later candidate ran: %v", err)
	}
	afterSource, sourceErr := os.ReadFile(filepath.Join(request.ProjectRoot, "value.go"))
	afterTest, testErr := os.ReadFile(filepath.Join(request.ProjectRoot, "value_test.go"))
	if sourceErr != nil || testErr != nil || !bytes.Equal(afterSource, []byte(source)) || !bytes.Equal(afterTest, []byte(testSource)) {
		t.Fatalf("fixture bytes changed: sourceErr=%v testErr=%v", sourceErr, testErr)
	}
}

func waitForFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
