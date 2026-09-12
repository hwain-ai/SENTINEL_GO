package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBridgeReportsActualKilledCandidatesAndRestoresSource(t *testing.T) {
	project := bridgeFixture(t, `package sample

func Positive(value int) bool { return value > 0 }
`, `package sample

import "testing"

func TestPositive(t *testing.T) {
	if Positive(0) || !Positive(1) { t.Fatal("wrong result") }
}
`)
	original, err := os.ReadFile(filepath.Join(project, "value.go"))
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{
		"--project-root", project,
		"--go-binary", filepath.Join(runtime.GOROOT(), "bin", "go"),
		"--runner-binary", buildRunner(t),
		"--timeout-ms", "10000",
		"--source", "value.go",
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	var report machineReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v: %q", err, stdout.String())
	}
	if len(report.SourceInventory) != 1 || report.SourceInventory[0].CandidateCount != 2 {
		t.Fatalf("source inventory = %#v", report.SourceInventory)
	}
	if len(report.Candidates) != 2 || len(report.Outcomes) != 2 {
		t.Fatalf("report = %#v", report)
	}
	for _, outcome := range report.Outcomes {
		if outcome.Status != "killed" {
			t.Fatalf("outcome = %#v", outcome)
		}
	}
	after, err := os.ReadFile(filepath.Join(project, "value.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatal("bridge did not restore source")
	}
}

func TestBridgePreservesSurvivedInsteadOfCallingItKilled(t *testing.T) {
	project := bridgeFixture(t, `package sample

func Positive(value int) bool { return value > 0 }
`, `package sample

import "testing"

func TestPositive(t *testing.T) {
	if !Positive(2) { t.Fatal("wrong result") }
}
`)
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{
		"--project-root", project,
		"--go-binary", filepath.Join(runtime.GOROOT(), "bin", "go"),
		"--runner-binary", buildRunner(t),
		"--timeout-ms", "10000",
		"--source", "value.go",
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	var report machineReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range report.Outcomes {
		if outcome.Status != "survived" {
			t.Fatalf("outcome = %#v", outcome)
		}
	}
}

func TestBridgeNeverCountsMutantPanicAsKilled(t *testing.T) {
	project := bridgeFixture(t, `package sample

func Positive(value int) bool { return value > 0 }
`, `package sample

import "testing"

func TestPositive(t *testing.T) {
	if Positive(0) { panic("mutant runtime failure") }
	if !Positive(1) { t.Fatal("assertion failure") }
}
`)
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{
		"--project-root", project,
		"--go-binary", filepath.Join(runtime.GOROOT(), "bin", "go"),
		"--runner-binary", buildRunner(t),
		"--timeout-ms", "10000",
		"--source", "value.go",
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	var report machineReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, outcome := range report.Outcomes {
		counts[outcome.Status]++
	}
	if counts["runtimeError"] != 1 || counts["killed"] != 1 {
		t.Fatalf("outcomes = %#v", report.Outcomes)
	}
}

func TestBridgeStopsBeforeMutationWhenBaselineFails(t *testing.T) {
	project := bridgeFixture(t, `package sample

func Positive(value int) bool { return value > 0 }
`, `package sample

import "testing"

func TestPositive(t *testing.T) { t.Fatal("baseline is red") }
`)
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{
		"--project-root", project,
		"--go-binary", filepath.Join(runtime.GOROOT(), "bin", "go"),
		"--runner-binary", buildRunner(t),
		"--timeout-ms", "10000",
		"--source", "value.go",
	}, &stdout, &stderr)
	if exitCode != 4 || stdout.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func buildRunner(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."))
	output := filepath.Join(t.TempDir(), "sentinel-go-test-runner")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-o", output, "./cmd/sentinel-go-test-runner")
	command.Dir = repositoryRoot
	command.Env = os.Environ()
	if payload, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v: %s", err, payload)
	}
	if err := os.Chmod(output, 0o700); err != nil {
		t.Fatal(err)
	}
	return output
}

func bridgeFixture(t *testing.T, source, tests string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":        "module example.test/bridge\n\ngo 1.21\n",
		"value.go":      source,
		"value_test.go": tests,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
