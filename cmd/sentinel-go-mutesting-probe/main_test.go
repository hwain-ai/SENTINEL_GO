package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHelpAndInvalidArgumentsDoNotRunTests(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "experimental") || !strings.Contains(out.String(), "--mutant-timeout-ms") {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &errOut)
	}
	for _, args := range [][]string{{}, {"--unknown"}, {"--project", "a"}, {"--project", "a", "--project", "b"}, {"--timeout-ms", "0"}} {
		out.Reset()
		errOut.Reset()
		if code := run(context.Background(), args, &out, &errOut); code != 3 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}

func TestSuccessfulProbeNeverExitsAsCertifiedPass(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":        "module sentinel.example/probe\n\ngo 1.27.0\n",
		"value.go":      "package probe\nfunc Positive(v int) bool { return v > 0 }\n",
		"value_test.go": "package probe\nimport \"testing\"\nfunc TestPositive(t *testing.T) { if !Positive(1) || Positive(0) || Positive(-1) {t.Fatal(\"wrong\")} }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--project", root, "--source", "value.go", "--go-binary", filepath.Join(runtime.GOROOT(), "bin/go"), "--timeout-ms", "20000"}
	var out, errOut bytes.Buffer
	if code := run(context.Background(), args, &out, &errOut); code != 6 {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &errOut)
	}
	var document map[string]any
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document["certified"] != false || !strings.Contains(errOut.String(), "backendNotAdmitted") {
		t.Fatal("probe was certified")
	}
	if _, err := os.Stat(filepath.Join(root, ".sentinel")); !os.IsNotExist(err) {
		t.Fatal("probe wrote evidence")
	}
}

func TestArgumentLimitsAndRepeatedSources(t *testing.T) {
	base := []string{"--project", "project", "--source", "a.go", "--go-binary", "go"}
	request, err := parse(append(base, "--source", "b.go", "--timeout-ms", "3600000"))
	if err != nil || len(request.Sources) != 2 || request.MutantTimeout != 0 {
		t.Fatalf("request=%v err=%v", request, err)
	}
	for _, option := range [][]string{{"--source", ""}, {"--unknown", "x"}, {"--go-binary", "other"}, {"--timeout-ms", "word"}, {"--timeout-ms", "3600001"}, {"--timeout-ms", "-1"}, {"--mutant-timeout-ms", "0"}, {"--mutant-timeout-ms", "word"}, {"--mutant-timeout-ms", "3600001"}, {"--mutant-timeout-ms", "9223372036854775808"}, {"--mutant-timeout-ms", "-1"}} {
		arguments := append(append([]string{}, base...), option...)
		if _, err := parse(arguments); err == nil {
			t.Fatalf("accepted %v", option)
		}
	}
}

func TestMutantTimeoutParsingIsExplicitUniqueAndOrderIndependent(t *testing.T) {
	base := []string{"--project", "project", "--source", "a.go", "--go-binary", "go"}
	for _, options := range [][]string{
		{"--timeout-ms", "100", "--mutant-timeout-ms", "75"},
		{"--mutant-timeout-ms", "75", "--timeout-ms", "100"},
	} {
		request, err := parse(append(append([]string{}, base...), options...))
		if err != nil || request.Timeout != 100*time.Millisecond || request.MutantTimeout != 75*time.Millisecond {
			t.Fatalf("options=%v request=%v err=%v", options, request, err)
		}
	}
	for _, options := range [][]string{
		{"--mutant-timeout-ms", "75", "--mutant-timeout-ms", "75"},
		{"--timeout-ms", "100", "--mutant-timeout-ms", "101"},
		{"--mutant-timeout-ms", "101", "--timeout-ms", "100"},
		{"--mutant-timeout-ms", "60001"},
	} {
		if _, err := parse(append(append([]string{}, base...), options...)); err == nil {
			t.Fatalf("accepted options %v", options)
		}
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("unwritable") }

func TestHelpWriteFailureAndFailureCodes(t *testing.T) {
	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, brokenWriter{}, &errOut); code != 6 {
		t.Fatalf("code=%d", code)
	}
	for name, want := range map[string]int{
		"goMutestingBaselineFailed": 4, "goMutestingDependencyInvalid": 5,
		"goMutestingCancelled": 8, "goMutestingSourceInvalid": 3, "goMutestingTimedOut": 6,
	} {
		if got := failureCode(name); got != want {
			t.Fatalf("code=%s got=%d want=%d", name, got, want)
		}
	}
}
