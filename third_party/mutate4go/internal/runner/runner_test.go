package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unclebob/mutate4go/internal/mutations"
)

func TestRunMutationsParallelUsesIsolatedWorkerCopies(t *testing.T) {
	root := t.TempDir()
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(previousDir)

	original := "package sample\n\nvar Flag = true\nvar Other = true\n"
	sourcePath := "sample.go"
	if err := os.WriteFile(sourcePath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	sites := []mutations.Site{
		booleanSite(0, original, "Flag = true"),
		booleanSite(1, original, "Other = true"),
	}
	results, err := runMutations(sourcePath, original, sites, time.Second, "! grep -q false sample.go", 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, result := range results {
		if result.Status != "killed" {
			t.Fatalf("expected killed result, got %#v", result)
		}
	}
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("parallel run changed source file:\n%s", content)
	}
	entries, err := os.ReadDir(filepath.Join(root, "target", "mutation-workers"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected worker run directory cleanup, found %d entries", len(entries))
	}
}

func TestCoverageCommandUsesConfiguredTestCommand(t *testing.T) {
	command := coverageCommand("go test ./internal/foo -run TestThing")
	expected := "go test ./internal/foo -run TestThing -coverprofile=target/coverage/coverage.out"
	if command != expected {
		t.Fatalf("expected %q, got %q", expected, command)
	}
}

func TestCopyProjectSkipsWorkerExcludedDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module sample\n")
	writeFile(t, filepath.Join(root, "pkg", "sample.go"), "package pkg\n")
	writeFile(t, filepath.Join(root, ".gocache", "cache"), "cache")
	writeFile(t, filepath.Join(root, ".gomodcache", "mod"), "mod")
	writeFile(t, filepath.Join(root, ".tools", "tool"), "tool")
	writeFile(t, filepath.Join(root, "target", "coverage", "coverage.out"), "coverage")
	writeFile(t, filepath.Join(root, "target", "mutation-workers", "old-run", "worker-1", "old"), "old")

	workerRoot := filepath.Join(root, "target", "mutation-workers", "run-1", "worker-1")
	if err := copyProject(root, workerRoot); err != nil {
		t.Fatal(err)
	}

	assertExists(t, filepath.Join(workerRoot, "go.mod"))
	assertExists(t, filepath.Join(workerRoot, "pkg", "sample.go"))
	assertNotExists(t, filepath.Join(workerRoot, ".gocache"))
	assertNotExists(t, filepath.Join(workerRoot, ".gomodcache"))
	assertNotExists(t, filepath.Join(workerRoot, ".tools"))
	assertNotExists(t, filepath.Join(workerRoot, "target", "coverage"))
	assertNotExists(t, filepath.Join(workerRoot, "target", "mutation-workers", "old-run"))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s not to exist, stat err=%v", path, err)
	}
}

func booleanSite(index int, content, marker string) mutations.Site {
	start := strings.Index(content, marker) + strings.Index(marker, "true")
	return mutations.Site{
		Index:       index,
		Line:        index + 3,
		StartOffset: start,
		EndOffset:   start + len("true"),
		Original:    "true",
		Mutant:      "false",
		Description: "true -> false",
		FunctionID:  "func/sample",
	}
}
