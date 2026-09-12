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

	"github.com/hwain-hwang/sentinel-go/internal/referenceexecution"
	"github.com/hwain-hwang/sentinel-go/internal/runner"
)

const commandRequestID = "abcdef0123456789abcdef0123456789"

func TestRunVersionIsExactAndDoesNotExecute(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	exitCode := runContextWithExecute(context.Background(), []string{"--version"}, &stdout, &stderr,
		func(context.Context, referenceexecution.Request) (referenceexecution.Receipt, error) {
			called = true
			return referenceexecution.Receipt{}, nil
		})
	if exitCode != 0 || stdout.String() != "sentinel-go-reference-runner/1\n" || stderr.Len() != 0 || called {
		t.Fatalf("exit=%d stdout=%q stderr=%q called=%v", exitCode, stdout.String(), stderr.String(), called)
	}
}

func TestRunVersionShortWriteIsReportFailure(t *testing.T) {
	stdout := &shortWriter{}
	var stderr bytes.Buffer
	exitCode := runContextWithExecute(context.Background(), []string{"--version"}, stdout, &stderr,
		func(context.Context, referenceexecution.Request) (referenceexecution.Receipt, error) {
			t.Fatal("version executed a project")
			return referenceexecution.Receipt{}, nil
		})
	if exitCode != 6 || stderr.String() != "referenceRunnerReportError\n" {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestParseRequestAcceptsSeparateAndEqualsFormsAtNumericBoundaries(t *testing.T) {
	cases := [][]string{
		{"--project-root", "relative-project", "--go-binary", "relative-go", "--request-id", commandRequestID, "--timeout-ms", "1"},
		{"--timeout-ms=86400000", "--request-id=" + commandRequestID, "--go-binary=relative-go", "--project-root=relative-project"},
	}
	for index, arguments := range cases {
		request, err := parseRequest(arguments)
		if err != nil {
			t.Fatalf("case %d: %v", index, err)
		}
		wantTimeout := time.Millisecond
		if index == 1 {
			wantTimeout = 24 * time.Hour
		}
		if request.ProjectRoot != "relative-project" || request.GoBinary != "relative-go" || request.RequestID != commandRequestID || request.Timeout != wantTimeout {
			t.Fatalf("case %d request=%#v", index, request)
		}
	}
}

func TestParseRequestRejectsNonExactCLIContract(t *testing.T) {
	valid := commandArguments("project", "go", "1000")
	cases := []struct {
		name      string
		arguments []string
	}{
		{name: "duplicate-separate", arguments: append(append([]string(nil), valid...), "--request-id", commandRequestID)},
		{name: "duplicate-equals", arguments: append(append([]string(nil), valid...), "--timeout-ms=1000")},
		{name: "unknown", arguments: append(append([]string(nil), valid...), "--coverage", "out")},
		{name: "positional", arguments: append(append([]string(nil), valid...), "extra")},
		{name: "missing-flag", arguments: valid[:len(valid)-2]},
		{name: "missing-value", arguments: append(append([]string(nil), valid[:len(valid)-1]...), "--timeout-ms")},
		{name: "empty-separate", arguments: commandArguments("", "go", "1000")},
		{name: "empty-equals", arguments: []string{"--project-root=project", "--go-binary=go", "--request-id=" + commandRequestID, "--timeout-ms="}},
		{name: "plus", arguments: commandArguments("project", "go", "+1")},
		{name: "minus", arguments: commandArguments("project", "go", "-1")},
		{name: "float", arguments: commandArguments("project", "go", "1.0")},
		{name: "exponent", arguments: commandArguments("project", "go", "1e3")},
		{name: "zero", arguments: commandArguments("project", "go", "0")},
		{name: "over-limit", arguments: commandArguments("project", "go", "86400001")},
		{name: "overflow", arguments: commandArguments("project", "go", "999999999999999999999999")},
		{name: "short-id", arguments: replaceArgument(valid, "--request-id", commandRequestID[:31])},
		{name: "uppercase-id", arguments: replaceArgument(valid, "--request-id", "Abcdef0123456789abcdef0123456789")},
		{name: "non-hex-id", arguments: replaceArgument(valid, "--request-id", "gbcdef0123456789abcdef0123456789")},
		{name: "version-with-other", arguments: append([]string{"--version"}, valid...)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if request, err := parseRequest(testCase.arguments); err == nil {
				t.Fatalf("accepted %#v", request)
			}
			var stdout, stderr bytes.Buffer
			called := false
			exitCode := runContextWithExecute(context.Background(), testCase.arguments, &stdout, &stderr,
				func(context.Context, referenceexecution.Request) (referenceexecution.Receipt, error) {
					called = true
					return referenceexecution.Receipt{}, nil
				})
			if exitCode != 3 || stdout.Len() != 0 || stderr.String() != "referenceRunnerUsageError\n" || called {
				t.Fatalf("exit=%d stdout=%q stderr=%q called=%v", exitCode, stdout.String(), stderr.String(), called)
			}
		})
	}
}

func TestRunEmitsCompletedTypedObservationsWithExitZero(t *testing.T) {
	cases := []struct {
		name       string
		testSource string
		status     runner.Status
	}{
		{name: "passed", status: runner.StatusPassed, testSource: "package fixture\nimport \"testing\"\nfunc TestPrivate(t *testing.T) {}\n"},
		{name: "assertion-failure", status: runner.StatusAssertionFailure, testSource: "package fixture\nimport \"testing\"\nfunc TestPrivate(t *testing.T) { t.Fail() }\n"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := commandFixture(t, testCase.testSource)
			var stdout, stderr bytes.Buffer
			exitCode := run(commandArguments(root, filepath.Join(runtime.GOROOT(), "bin", "go"), "20000"), &stdout, &stderr)
			if exitCode != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
			}
			var receipt referenceexecution.Receipt
			if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Status != testCase.status || receipt.RequestID != commandRequestID {
				t.Fatalf("receipt=%#v", receipt)
			}
			for _, secret := range []string{root, "TestPrivate", "example.test/command-private"} {
				if strings.Contains(stdout.String(), secret) {
					t.Errorf("output leaked %q", secret)
				}
			}
		})
	}

	for _, status := range []runner.Status{runner.StatusRuntimeError, runner.StatusCompileError, runner.StatusTimedOut, runner.StatusToolError} {
		t.Run(string(status), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exitCode := runContextWithExecute(context.Background(), commandArguments("project", "go", "1000"), &stdout, &stderr,
				func(context.Context, referenceexecution.Request) (referenceexecution.Receipt, error) {
					receipt := sampleReceipt()
					receipt.Status = status
					return receipt, nil
				})
			if exitCode != 0 || stderr.Len() != 0 {
				t.Fatalf("status=%q exit=%d stdout=%q stderr=%q", status, exitCode, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunExecutionAndCancellationUseFixedDiagnosticAndNoReceipt(t *testing.T) {
	t.Run("execution", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		exitCode := runContextWithExecute(context.Background(), commandArguments("project", "go", "1000"), &stdout, &stderr,
			func(context.Context, referenceexecution.Request) (referenceexecution.Receipt, error) {
				return referenceexecution.Receipt{}, errors.New("raw /secret/path TestPrivate")
			})
		if exitCode != 6 || stdout.Len() != 0 || stderr.String() != "referenceRunnerError\n" {
			t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
		}
	})

	t.Run("final-cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var stdout, stderr bytes.Buffer
		exitCode := runContextWithExecute(ctx, commandArguments("project", "go", "1000"), &stdout, &stderr,
			func(context.Context, referenceexecution.Request) (referenceexecution.Receipt, error) {
				cancel()
				return sampleReceipt(), nil
			})
		if exitCode != 6 || stdout.Len() != 0 || stderr.String() != "referenceRunnerError\n" {
			t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
		}
	})
}

func TestRunOutputFailureLeavesNoCompleteJSONReceipt(t *testing.T) {
	stdout := &prefixFailWriter{limit: 20}
	var stderr bytes.Buffer
	exitCode := runContextWithExecute(context.Background(), commandArguments("project", "go", "1000"), stdout, &stderr,
		func(context.Context, referenceexecution.Request) (referenceexecution.Receipt, error) {
			return sampleReceipt(), nil
		})
	if exitCode != 6 || stderr.String() != "referenceRunnerReportError\n" || json.Valid(stdout.payload.Bytes()) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.payload.String(), stderr.String())
	}
}

type prefixFailWriter struct {
	limit   int
	payload bytes.Buffer
}

type shortWriter struct{}

func (*shortWriter) Write(payload []byte) (int, error) {
	return len(payload) - 1, nil
}

func (writer *prefixFailWriter) Write(payload []byte) (int, error) {
	count := writer.limit
	if count > len(payload) {
		count = len(payload)
	}
	_, _ = writer.payload.Write(payload[:count])
	return count, errors.New("raw output failure")
}

func commandArguments(project, goBinary, timeout string) []string {
	return []string{"--project-root", project, "--go-binary", goBinary, "--request-id", commandRequestID, "--timeout-ms", timeout}
}

func replaceArgument(arguments []string, name, value string) []string {
	result := append([]string(nil), arguments...)
	for index := range result {
		if result[index] == name && index+1 < len(result) {
			result[index+1] = value
			return result
		}
	}
	return result
}

func commandFixture(t *testing.T, testSource string) string {
	t.Helper()
	root := t.TempDir()
	writeCommandFile(t, filepath.Join(root, "go.mod"), "module example.test/command-private\n\ngo 1.27.0\n")
	writeCommandFile(t, filepath.Join(root, "value.go"), "package fixture\nfunc Value() int { return 1 }\n")
	writeCommandFile(t, filepath.Join(root, "value_test.go"), testSource)
	return root
}

func writeCommandFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sampleReceipt() referenceexecution.Receipt {
	return referenceexecution.Receipt{
		SchemaVersion: "sentinel-go-reference-execution-v1", Profile: "replay-identity-v1", RequestID: commandRequestID,
		TimeoutMilliseconds: 1000, RunnerSchema: runner.RunnerSchema, Nonce: "nonce", Status: runner.StatusToolError,
		InventoryCount: 1, InputSHA256: strings.Repeat("a", 64), ReferenceOnly: true,
	}
}
