package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/mutation"
)

func TestMutantTimeoutOrchestratorForwardsExactBridgeArguments(t *testing.T) {
	project := t.TempDir()
	source := "package sample\nfunc Positive(x int) bool { return x > 0 }\n"
	if err := os.WriteFile(filepath.Join(project, "value.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	report := fmt.Sprintf(`{"schemaVersion":"sentinel-mutate4go-report-v1","backendName":"mutate4go","backendCommit":"9016c7adafc1c7e282b5e27768e732e477713af8","sourceInventory":[{"path":"value.go","sha256":"%x","candidateCount":1}],"candidates":[{"id":"candidate","sourceFile":"value.go","line":2,"column":1,"operator":"> -> >="}],"outcomes":[{"candidateId":"candidate","status":"killed","durationNanos":1}]}`, sha256.Sum256([]byte(source)))
	argv := filepath.Join(t.TempDir(), "argv")
	backend := filepath.Join(t.TempDir(), "recorder")
	script := fmt.Sprintf("#!/usr/bin/bash\nset -eu\n/usr/bin/printf '%%s\\n' \"$@\" > %q\n/usr/bin/printf '%%s\\n' '%s'\n", argv, report)
	if err := os.WriteFile(backend, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	for _, selected := range []time.Duration{0, 1250 * time.Millisecond} {
		request := MutationRequest{ProjectRoot: project, BackendExecutable: backend, RunnerExecutable: backend, GoBinary: goBinary, Timeout: 3 * time.Second, MutantTimeout: selected}
		got, err := RunMutation(context.Background(), request)
		if err != nil || !got.Gate.Pass {
			t.Fatalf("report=%+v error=%v", got, err)
		}
		payload, err := os.ReadFile(argv)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"--project-root", ".", "--go-binary", goBinary, "--runner-binary", backend, "--timeout-ms", "3000", "--source", "value.go"}
		if selected > 0 {
			want = append(want, "--mutant-timeout-ms", "1250")
		}
		if arguments := strings.Split(strings.TrimSpace(string(payload)), "\n"); !slices.Equal(arguments, want) {
			t.Errorf("arguments=%q want=%q", arguments, want)
		}
	}
}

func TestMutantTimeoutActualBridgeCancellationDiscardsReportAndRemovesSnapshot(t *testing.T) {
	backend, runner := buildMutationBridge(t), buildTypedRunner(t)
	for _, parentCancellation := range []bool{true, false} {
		t.Run(fmt.Sprintf("parent=%t", parentCancellation), func(t *testing.T) {
			request, marker, calls, snapshotPath := mutantCancellationFixture(t, backend, runner)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var report MutationReport
			var runErr error
			done := make(chan struct{})
			go func() {
				report, runErr = RunMutation(ctx, request)
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(8 * time.Second):
					t.Error("adapter did not finish bounded fixture cleanup")
				}
			})
			pid := waitMutantFixturePID(t, marker)
			if parentCancellation {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("adapter did not finish after cancellation/overall expiry")
			}
			want := "backendTimedOut"
			if parentCancellation {
				want = "backendProcessFailed"
			}
			var classified *mutation.AdapterError
			if !errors.As(runErr, &classified) || classified.Code != want || classified.ExitCode != 6 || report.BackendCommit != "" || len(report.Mutants) != 0 {
				t.Errorf("report=%+v error=%v classification=%+v", report, runErr, classified)
			}
			if !mutantFixtureStopped(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Error("actual Go-test process still running before fallback cleanup")
			}
			payload, err := os.ReadFile(calls)
			if err != nil || len(strings.Fields(string(payload))) != 4 {
				t.Errorf("expected coverage, two controls, one replay; calls=%q error=%v", payload, err)
			}
			payload, err = os.ReadFile(snapshotPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(strings.TrimSpace(string(payload))); !os.IsNotExist(err) {
				t.Errorf("snapshot was not removed: %v", err)
			}
		})
	}
}

func mutantCancellationFixture(t *testing.T, backend, runner string) (MutationRequest, string, string, string) {
	t.Helper()
	project, state := t.TempDir(), t.TempDir()
	marker, calls, snapshotPath := filepath.Join(state, "test-pid"), filepath.Join(state, "runner-calls"), filepath.Join(state, "snapshot")
	files := map[string]string{
		"go.mod":        "module example.test/cancellation\n\ngo 1.27.0\n",
		"value.go":      "package sample\nfunc Positive(x int) bool { return x > 0 }\n",
		"value_test.go": fmt.Sprintf("package sample\nimport (\"testing\"; \"time\"; \"os\"; \"strconv\")\nfunc TestPositive(t *testing.T) { if Positive(0) { if err := os.WriteFile(%q, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil { t.Fatal(err) }; time.Sleep(15*time.Second); t.Fatal(\"hung mutant\") }; if !Positive(1) { t.Fatal(\"later mutant\") } }\n", marker),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(project, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for name, original := range files {
			payload, err := os.ReadFile(filepath.Join(project, name))
			if err != nil || !bytes.Equal(payload, []byte(original)) {
				t.Errorf("original %s changed: error=%v", name, err)
			}
		}
	})
	backendWrapper, runnerWrapper := filepath.Join(state, "bridge"), filepath.Join(state, "runner")
	for path, body := range map[string]string{
		backendWrapper: fmt.Sprintf("pwd > %q\nexec %q \"$@\"", snapshotPath, backend),
		runnerWrapper:  fmt.Sprintf("printf 'call\\n' >> %q\nexec %q \"$@\"", calls, runner),
	} {
		if err := os.WriteFile(path, []byte("#!/usr/bin/bash\nset -eu\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return MutationRequest{ProjectRoot: project, BackendExecutable: backendWrapper, RunnerExecutable: runnerWrapper, GoBinary: filepath.Join(runtime.GOROOT(), "bin", "go"), Timeout: 6 * time.Second, MutantTimeout: 6 * time.Second}, marker, calls, snapshotPath
}

func waitMutantFixturePID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		payload, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(payload)))
			if parseErr != nil || pid < 1 {
				t.Fatalf("invalid fixture pid=%q error=%v", payload, parseErr)
			}
			return pid
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("actual mutant did not start before fixture deadline")
	return 0
}

func mutantFixtureStopped(pid int) bool {
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		payload, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) || (err == nil && strings.Contains(string(payload), ") Z ")) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}
