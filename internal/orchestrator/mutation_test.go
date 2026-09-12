package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRunMutationUsesActualVendoredBridgeAndStrictGate(t *testing.T) {
	backend := buildMutationBridge(t)
	runnerBinary := buildTypedRunner(t)
	project := crapProjectFixture(t)

	report, err := RunMutation(context.Background(), MutationRequest{
		ProjectRoot:       project,
		Sources:           []string{"value.go"},
		BackendExecutable: backend,
		RunnerExecutable:  runnerBinary,
		GoBinary:          filepath.Join(runtime.GOROOT(), "bin", "go"),
		Timeout:           60 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Gate.Pass || report.Gate.InScope != 4 || len(report.Mutants) != 4 {
		t.Fatalf("report = %#v", report)
	}
}

func TestRunMutationPreservesSurvivorsAsQualityFailure(t *testing.T) {
	backend := buildMutationBridge(t)
	runnerBinary := buildTypedRunner(t)
	project := t.TempDir()
	files := map[string]string{
		"go.mod":   "module example.test/survivor\n\ngo 1.27.0\n",
		"value.go": "package survivor\n\nfunc Positive(value int) bool { return value > 0 }\n",
		"value_test.go": `package survivor

import "testing"

func TestPositive(t *testing.T) {
	if !Positive(2) { t.Fatal("wrong result") }
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(project, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	report, err := RunMutation(context.Background(), MutationRequest{
		ProjectRoot:       project,
		Sources:           []string{"value.go"},
		BackendExecutable: backend,
		RunnerExecutable:  runnerBinary,
		GoBinary:          filepath.Join(runtime.GOROOT(), "bin", "go"),
		Timeout:           60 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Gate.Pass || report.Gate.Counts["survived"] == 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestResolveMutationSourcesRejectsPartialExplicitInventory(t *testing.T) {
	project := t.TempDir()
	for name, content := range map[string]string{
		"first.go": "package sample\n\nfunc First() int { return 1 }\n",
		"last.go":  "package sample\n\nfunc Last() int { return 2 }\n",
	} {
		if err := os.WriteFile(filepath.Join(project, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, err := resolveMutationSources(MutationRequest{
		ProjectRoot: project,
		Sources:     []string{"first.go"},
	})
	if err == nil || err.Error() != "mutationSourceInventoryMismatch" {
		t.Fatalf("partial explicit inventory error = %v", err)
	}
}

func TestResolveMutationSourcesAcceptsTheCompleteInventoryInCanonicalOrder(t *testing.T) {
	project := t.TempDir()
	for _, name := range []string{"first.go", "last.go"} {
		if err := os.WriteFile(
			filepath.Join(project, name),
			[]byte("package sample\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	sources, err := resolveMutationSources(MutationRequest{
		ProjectRoot: project,
		Sources:     []string{"last.go", "first.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0] != "first.go" || sources[1] != "last.go" {
		t.Fatalf("canonical sources = %q", sources)
	}
}

func buildTypedRunner(t *testing.T) string {
	t.Helper()
	repositoryRoot := testRepositoryRoot(t)
	runnerBinary := filepath.Join(t.TempDir(), "sentinel-go-test-runner")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-o", runnerBinary, "./cmd/sentinel-go-test-runner")
	command.Dir = repositoryRoot
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build typed runner: %v: %s", err, output)
	}
	if err := os.Chmod(runnerBinary, 0o700); err != nil {
		t.Fatal(err)
	}
	return runnerBinary
}

func buildMutationBridge(t *testing.T) string {
	t.Helper()
	repositoryRoot := testRepositoryRoot(t)
	backend := filepath.Join(t.TempDir(), "sentinel-mutate4go-bridge")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "-C", filepath.Join(repositoryRoot, "third_party", "mutate4go"), "build", "-trimpath", "-o", backend, "./cmd/sentinel-mutate4go-bridge")
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build bridge: %v: %s", err, output)
	}
	if err := os.Chmod(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	return backend
}

func testRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}
