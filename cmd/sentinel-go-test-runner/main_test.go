package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestRunCoversVersionUsageRunnerAndReportOutcomes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"--version"}, &stdout, &stderr); exitCode != 0 || strings.TrimSpace(stdout.String()) != version {
		t.Fatalf("version exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if exitCode := run([]string{"--unknown"}, &stdout, &stderr); exitCode != 3 || !strings.Contains(stderr.String(), "runnerUsageError") {
		t.Fatalf("usage exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}

	project := runnerCommandFixture(t)
	arguments := runnerArguments(project)
	stdout.Reset()
	stderr.Reset()
	if exitCode := run(arguments, &stdout, &stderr); exitCode != 0 || !strings.Contains(stdout.String(), `"status":"passed"`) {
		t.Fatalf("success exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}

	stderr.Reset()
	if exitCode := run(arguments, failingWriter{}, &stderr); exitCode != 6 || !strings.Contains(stderr.String(), "runnerReportError") {
		t.Fatalf("report exit=%d stderr=%q", exitCode, stderr.String())
	}

	emptyProject := t.TempDir()
	writeRunnerCommandFile(t, filepath.Join(emptyProject, "go.mod"), "module example.test/empty\n\ngo 1.27.0\n")
	stdout.Reset()
	stderr.Reset()
	if exitCode := run(runnerArguments(emptyProject), &stdout, &stderr); exitCode != 6 || !strings.Contains(stderr.String(), "runnerError") {
		t.Fatalf("runner exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestParseRequestRejectsTrailingMissingAndZeroTimeout(t *testing.T) {
	valid := runnerArguments(t.TempDir())
	request, err := parseRequest(valid)
	if err != nil || request.Timeout.Milliseconds() != 20_000 {
		t.Fatalf("request=%#v error=%v", request, err)
	}
	for _, arguments := range [][]string{
		append(append([]string(nil), valid...), "trailing"),
		{"--project-root", "/tmp", "--go-binary", filepath.Join(runtime.GOROOT(), "bin", "go")},
		{"--project-root", "/tmp", "--go-binary", filepath.Join(runtime.GOROOT(), "bin", "go"), "--timeout-ms", "0"},
	} {
		if _, err := parseRequest(arguments); err == nil {
			t.Fatalf("parseRequest(%q) succeeded", arguments)
		}
	}
}

func runnerCommandFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeRunnerCommandFile(t, filepath.Join(root, "go.mod"), "module example.test/command\n\ngo 1.27.0\n")
	writeRunnerCommandFile(t, filepath.Join(root, "value.go"), "package command\n\nfunc Value() int { return 1 }\n")
	writeRunnerCommandFile(t, filepath.Join(root, "value_test.go"), "package command\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 1 { t.Fail() } }\n")
	return root
}

func runnerArguments(project string) []string {
	return []string{
		"--project-root", project,
		"--go-binary", filepath.Join(runtime.GOROOT(), "bin", "go"),
		"--timeout-ms", "20000",
	}
}

func writeRunnerCommandFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
