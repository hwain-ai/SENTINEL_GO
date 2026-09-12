package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestExecuteClassifiesTypedGoFailuresAndRestoresTestSource(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status Status
	}{
		{"pass", "", StatusPassed},
		{"fail", "t.Fail()", StatusAssertionFailure},
		{"fail-now", "t.FailNow()", StatusAssertionFailure},
		{"panic", `panic("boom")`, StatusRuntimeError},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			project, testPath, original := runnerFixture(t, testCase.body)
			report, err := Execute(context.Background(), Request{
				ProjectRoot: project,
				GoBinary:    filepath.Join(runtime.GOROOT(), "bin", "go"),
				Timeout:     20 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			if report.Status != testCase.status || report.InventoryCount != 1 || report.Nonce == "" {
				t.Fatalf("report = %#v", report)
			}
			after, err := os.ReadFile(testPath)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatalf("test source was not restored: error=%v", err)
			}
		})
	}
}

func TestExecuteCoversSubtestChildPanicExampleAndProcessAbort(t *testing.T) {
	cases := []struct {
		name   string
		source string
		status Status
	}{
		{
			name: "subtest-fail-now",
			source: `package runner

import "testing"

func TestValue(t *testing.T) { t.Run("child", func(t *testing.T) { t.FailNow() }) }
`,
			status: StatusAssertionFailure,
		},
		{
			name: "child-goroutine-panic",
			source: `package runner

import "testing"

func TestValue(t *testing.T) {
	done := make(chan struct{})
	go func() { defer close(done); panic("child") }()
	<-done
}
`,
			status: StatusRuntimeError,
		},
		{
			name: "example-output-mismatch",
			source: `package runner

import "fmt"

func ExampleValue() {
	fmt.Println("actual")
	// Output: expected
}
`,
			status: StatusAssertionFailure,
		},
		{
			name: "process-abort",
			source: `package runner

import (
	"os"
	"testing"
)

func TestValue(t *testing.T) { os.Exit(9) }
`,
			status: StatusRuntimeError,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			project, testPath, original := runnerSourceFixture(t, testCase.source)
			report, err := Execute(context.Background(), Request{
				ProjectRoot: project,
				GoBinary:    filepath.Join(runtime.GOROOT(), "bin", "go"),
				Timeout:     20 * time.Second,
			})
			if err != nil || report.Status != testCase.status {
				t.Fatalf("report=%#v error=%v", report, err)
			}
			after, readErr := os.ReadFile(testPath)
			if readErr != nil || !bytes.Equal(after, original) {
				t.Fatalf("test source was not restored: error=%v", readErr)
			}
		})
	}
}

func TestExecuteRejectsUnsupportedBenchmarkBeforeRunningTests(t *testing.T) {
	project, testPath, original := runnerSourceFixture(t, `package runner

import "testing"

func BenchmarkValue(b *testing.B) { panic("must not run") }
`)
	if _, err := Execute(context.Background(), Request{
		ProjectRoot: project,
		GoBinary:    filepath.Join(runtime.GOROOT(), "bin", "go"),
		Timeout:     20 * time.Second,
	}); err == nil {
		t.Fatal("unsupported benchmark was accepted")
	}
	after, err := os.ReadFile(testPath)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("preflight changed source: error=%v", err)
	}
}

func TestExecuteClassifiesFuzzSeedFailureAsAssertion(t *testing.T) {
	project, testPath, original := runnerSourceFixture(t, `package runner

import "testing"

func FuzzValue(f *testing.F) {
	f.Add(1)
	f.Fuzz(func(t *testing.T, value int) {
		if value == 1 { t.FailNow() }
	})
}
`)
	report, err := Execute(context.Background(), Request{
		ProjectRoot: project,
		GoBinary:    filepath.Join(runtime.GOROOT(), "bin", "go"),
		Timeout:     20 * time.Second,
	})
	if err != nil || report.Status != StatusAssertionFailure {
		t.Fatalf("report=%#v error=%v", report, err)
	}
	after, readErr := os.ReadFile(testPath)
	if readErr != nil || !bytes.Equal(after, original) {
		t.Fatalf("test source was not restored: error=%v", readErr)
	}
}

func TestExecuteClassifiesCleanupFailureAsAssertion(t *testing.T) {
	project, testPath, original := runnerSourceFixture(t, `package runner

import "testing"

func TestValue(t *testing.T) {
	t.Cleanup(func() { t.FailNow() })
}
`)
	report, err := Execute(context.Background(), Request{
		ProjectRoot: project,
		GoBinary:    filepath.Join(runtime.GOROOT(), "bin", "go"),
		Timeout:     20 * time.Second,
	})
	if err != nil || report.Status != StatusAssertionFailure {
		t.Fatalf("report=%#v error=%v", report, err)
	}
	after, readErr := os.ReadFile(testPath)
	if readErr != nil || !bytes.Equal(after, original) {
		t.Fatalf("test source was not restored: error=%v", readErr)
	}
}

func TestExecuteDoesNotHideBuildFailureBehindAssertion(t *testing.T) {
	project, _, _ := runnerFixture(t, "t.Fail()")
	brokenDirectory := filepath.Join(project, "broken")
	if err := os.Mkdir(brokenDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDirectory, "broken.go"), []byte("package broken\n\nfunc Broken( {\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Execute(context.Background(), Request{
		ProjectRoot: project,
		GoBinary:    filepath.Join(runtime.GOROOT(), "bin", "go"),
		Timeout:     20 * time.Second,
	})
	if err != nil || report.Status != StatusCompileError {
		t.Fatalf("report=%#v error=%v", report, err)
	}
}

func runnerFixture(t *testing.T, body string) (string, string, []byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/runner\n\ngo 1.27.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "value.go"), []byte("package runner\n\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testPath := filepath.Join(root, "value_test.go")
	original := []byte("package runner\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { " + body + " }\n")
	if err := os.WriteFile(testPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, testPath, original
}

func runnerSourceFixture(t *testing.T, testSource string) (string, string, []byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/runner\n\ngo 1.27.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "value.go"), []byte("package runner\n\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testPath := filepath.Join(root, "value_test.go")
	original := []byte(testSource)
	if err := os.WriteFile(testPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, testPath, original
}
