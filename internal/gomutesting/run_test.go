package gomutesting

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/mutation"
	"github.com/hwain-hwang/sentinel-go/internal/runner"
)

func projectFixture(t *testing.T, source, body string) Request {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":        "module sentinel.example/probe\n\ngo 1.27.0\n",
		"value.go":      source,
		"value_test.go": "package probe\nimport \"testing\"\nfunc TestPositive(t *testing.T) {" + body + "}\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Request{ProjectRoot: root, Sources: []string{"value.go"}, GoBinary: filepath.Join(runtime.GOROOT(), "bin/go"), Timeout: 20 * time.Second}
}

func TestExternalLibraryRunsStrongWeakAndPanicTestsWithoutCertifying(t *testing.T) {
	cases := []struct {
		name, body                     string
		killed, survived, runtimeError int
	}{
		{"strong", `if !Positive(1) || Positive(0) || Positive(-1) { t.Fatal("wrong") }`, 3, 0, 0},
		{"weak", `if !Positive(1) { t.Fatal("wrong") }`, 1, 2, 0},
		{"panic", `if !Positive(1) || Positive(0) || Positive(-1) { panic("wrong") }`, 0, 0, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			request := projectFixture(t, positiveSource, c.body)
			result, err := Run(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Certified || result.Profile != Profile || len(result.Records) != 3 {
				t.Fatalf("unexpected result: %#v", result)
			}
			if result.Counts[mutation.Killed] != c.killed || result.Counts[mutation.Survived] != c.survived || result.Counts[mutation.RuntimeError] != c.runtimeError {
				t.Fatalf("counts=%v", result.Counts)
			}
			actual, err := os.ReadFile(filepath.Join(request.ProjectRoot, "value.go"))
			if err != nil || !bytes.Equal(actual, []byte(positiveSource)) {
				t.Fatal("original changed")
			}
			entries, err := os.ReadDir(request.ProjectRoot)
			if err != nil || len(entries) != 3 {
				t.Fatal("original project state changed")
			}
			payload, err := json.Marshal(result)
			if err != nil || strings.Contains(string(payload), "return value") || strings.Contains(string(payload), request.ProjectRoot) || strings.Contains(string(payload), `"pass":true`) {
				t.Fatal("probe output leaks source, path, or certified pass")
			}
		})
	}
}

func TestRunRejectsBaselineFailureEmptyPlanAndCancelledRequest(t *testing.T) {
	request := projectFixture(t, positiveSource, `t.Fatal("broken baseline")`)
	if _, err := Run(context.Background(), request); err == nil || err.Error() != "goMutestingBaselineFailed" {
		t.Fatalf("err=%v", err)
	}
	request = projectFixture(t, "package probe\ntype Value struct{}\n", "")
	result, err := Run(context.Background(), request)
	if err != nil || len(result.Records) != 0 || result.Certified {
		t.Fatalf("result=%v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, request); err == nil {
		t.Fatal("cancelled request ran")
	}
	request.Sources = []string{"value.go", "value.go"}
	if _, err := Run(context.Background(), request); err == nil {
		t.Fatal("duplicate source accepted")
	}
}

func TestReplayClassificationKeepsFailuresDistinct(t *testing.T) {
	cases := []struct {
		raw  runner.Status
		want mutation.Status
	}{
		{runner.StatusPassed, mutation.Survived}, {runner.StatusAssertionFailure, mutation.Killed},
		{runner.StatusRuntimeError, mutation.RuntimeError}, {runner.StatusCompileError, mutation.CompileError},
		{runner.StatusTimedOut, mutation.TimedOut}, {runner.StatusToolError, mutation.ToolError},
	}
	for _, c := range cases {
		first := proofReport(c.raw, "a")
		second := first
		second.Nonce = strings.Repeat("b", 32)
		if got := classify(first, second); got != c.want {
			t.Fatalf("got=%s want=%s", got, c.want)
		}
		if classify(first, first) != mutation.ToolError {
			t.Fatal("reused execution proof accepted")
		}
		second.InventoryCount = 2
		if classify(first, second) != mutation.ToolError {
			t.Fatal("changed test inventory accepted")
		}
	}
}

func TestCompileErrorsAreNotKilled(t *testing.T) {
	request := projectFixture(t, "package probe\nvar Values [0]int\n", `if len(Values) != 0 { t.Fatal("wrong") }`)
	result, err := Run(context.Background(), request)
	if err != nil || result.Counts[mutation.CompileError] != 1 || result.Counts[mutation.Killed] != 1 {
		t.Fatalf("counts=%v err=%v", result.Counts, err)
	}
}

func TestTimeoutDiscardsReportAndPreservesOriginal(t *testing.T) {
	request := projectFixture(t, positiveSource, `for {}`)
	request.Timeout = 1200 * time.Millisecond
	result, err := Run(context.Background(), request)
	if err == nil || err.Error() != "goMutestingTimedOut" || len(result.Records) != 0 {
		t.Fatalf("result=%v err=%v", result, err)
	}
	actual, err := os.ReadFile(filepath.Join(request.ProjectRoot, "value.go"))
	if err != nil || string(actual) != positiveSource {
		t.Fatal("timeout changed original source")
	}
}

func TestTestSideSourceChangesRejectReport(t *testing.T) {
	request := projectFixture(t, positiveSource, "")
	testSource := "package probe\nimport (\"os\"; \"testing\")\nfunc TestPositive(t *testing.T) { if err := os.WriteFile(\"value.go\", []byte(\"package probe\\n\"), 0600); err != nil { t.Fatal(err) } }\n"
	if err := os.WriteFile(filepath.Join(request.ProjectRoot, "value_test.go"), []byte(testSource), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), request)
	if err == nil || err.Error() != "goMutestingSourceChanged" || len(result.Records) != 0 {
		t.Fatalf("result=%v err=%v", result, err)
	}
	actual, err := os.ReadFile(filepath.Join(request.ProjectRoot, "value.go"))
	if err != nil || string(actual) != positiveSource {
		t.Fatal("test side effect reached original source")
	}
}

func TestRequestAndSourceFailuresHaveStableCodes(t *testing.T) {
	request := projectFixture(t, positiveSource, "")
	for _, duration := range []time.Duration{0, -1, time.Hour + 1} {
		invalid := request
		invalid.Timeout = duration
		if _, err := Run(context.Background(), invalid); err == nil || err.Error() != "goMutestingRequestInvalid" {
			t.Fatalf("timeout=%s err=%v", duration, err)
		}
	}
	for _, duration := range []time.Duration{-1, request.Timeout + time.Nanosecond, time.Hour + time.Nanosecond} {
		invalid := request
		invalid.MutantTimeout = duration
		invalid.ProjectRoot = filepath.Join(request.ProjectRoot, "missing")
		invalid.Sources = []string{"missing.go"}
		invalid.GoBinary = "/not-a-sentinel-toolchain/go"
		if _, err := Run(context.Background(), invalid); err == nil || err.Error() != "goMutestingRequestInvalid" {
			t.Fatalf("mutant timeout=%s err=%v", duration, err)
		}
	}
	request.GoBinary = "/not-a-sentinel-toolchain/go"
	if _, err := Run(context.Background(), request); err == nil || err.Error() != "goMutestingDependencyInvalid" {
		t.Fatalf("err=%v", err)
	}
	request.GoBinary = filepath.Join(runtime.GOROOT(), "bin/go")
	request.Sources = []string{"missing.go"}
	if _, err := Run(context.Background(), request); err == nil || err.Error() != "goMutestingSourceMissing" {
		t.Fatalf("err=%v", err)
	}
}
