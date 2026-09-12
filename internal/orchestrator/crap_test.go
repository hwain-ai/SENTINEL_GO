package orchestrator

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAnalyzeCrapUsesActualGoCoverageAndLeavesProjectUnchanged(t *testing.T) {
	project := crapProjectFixture(t)
	profile := filepath.Join(t.TempDir(), "coverage.out")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "test", "./...", "-count=1", "-coverprofile="+profile)
	command.Dir = project
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("coverage failed: %v: %s", err, output)
	}
	original, err := os.ReadFile(filepath.Join(project, "value.go"))
	if err != nil {
		t.Fatal(err)
	}

	report, err := AnalyzeCrap(project, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Pass || len(report.Rows) != 1 {
		t.Fatalf("report = %#v", report)
	}
	row := report.Rows[0]
	if row.Complexity != 2 || row.CoveredUnits == 0 || row.TotalUnits == 0 || !row.Pass || row.UnknownReason != "" {
		t.Fatalf("row = %#v", row)
	}
	after, err := os.ReadFile(filepath.Join(project, "value.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatal("CRAP analysis modified source")
	}
}

func TestAnalyzeCrapDoesNotHideMissingCoverage(t *testing.T) {
	project := crapProjectFixture(t)
	profile := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(profile, []byte("mode: set\nexample.test/sample/other.go:1.1,1.2 1 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := AnalyzeCrap(project, profile)
	if err != nil {
		t.Fatal(err)
	}
	if report.Pass || len(report.Rows) != 1 || report.Rows[0].UnknownReason != "coverageFileMissing" {
		t.Fatalf("report = %#v", report)
	}
}

func TestProductionInventoryExcludesTestsFixturesAndGeneratedRoots(t *testing.T) {
	root := t.TempDir()
	writeInventoryFile(t, filepath.Join(root, "value.go"))
	writeInventoryFile(t, filepath.Join(root, "value_test.go"))
	for _, directory := range []string{"testdata", "vendor", "third_party", "target", ".sentinel", ".toolchain"} {
		writeInventoryFile(t, filepath.Join(root, directory, "ignored.go"))
	}
	sources, err := productionGoSources(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0] != "value.go" {
		t.Fatalf("sources = %q, want only value.go", sources)
	}
}

func writeInventoryFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func crapProjectFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.test/sample\n\ngo 1.27.0\n",
		"value.go": `package sample

func Positive(value int) bool {
	if value > 0 {
		return true
	}
	return false
}
`,
		"value_test.go": `package sample

import "testing"

func TestPositive(t *testing.T) {
	if Positive(0) || !Positive(1) {
		t.Fatal("wrong result")
	}
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
