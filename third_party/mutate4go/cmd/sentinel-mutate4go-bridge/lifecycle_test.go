package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const lifecycleTimedOut = `{"schemaVersion":"sentinel-go-typed-runner-v1","nonce":"0123456789abcdef0123456789abcdef","status":"timedOut","inventoryCount":1}`

func bridgeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte("#!/usr/bin/bash\nset -eu\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLifecycleTypedRunnerReportGrace(t *testing.T) {
	options := bridgeOptions{projectRoot: t.TempDir(), timeout: 50 * time.Millisecond}
	emitted := filepath.Join(t.TempDir(), "emitted")
	options.runnerBinary = bridgeScript(t, fmt.Sprintf("sleep .15\ntouch %q\nprintf '%%s' '%s'", emitted, lifecycleTimedOut))
	if status := runTypedTests(options, ""); status != "timedOut" {
		t.Fatalf("status=%s", status)
	}
	if _, err := os.Stat(emitted); err != nil {
		t.Fatal("valid typed report was not emitted during grace")
	}
}

func TestLifecycleTypedRunnerWatchdogIsToolError(t *testing.T) {
	for _, body := range []string{"sleep 6", "head -c 16777217 /dev/zero\nsleep 6"} {
		options := bridgeOptions{projectRoot: t.TempDir(), timeout: 50 * time.Millisecond, runnerBinary: bridgeScript(t, body)}
		if status := runTypedTests(options, ""); status != "toolError" {
			t.Errorf("watchdog status=%s", status)
		}
	}
}

func TestLifecycleTypedRunnerRejectsOversizedOtherwiseValidReport(t *testing.T) {
	passed := strings.Replace(lifecycleTimedOut, "timedOut", "passed", 1)
	// Whitespace is legal JSON padding: only the process-output bound rejects it.
	script := bridgeScript(t, "head -c 16777217 /dev/zero | tr '\\000' ' '\nprintf '%s' '"+passed+"'")
	options := bridgeOptions{projectRoot: t.TempDir(), timeout: time.Second, runnerBinary: script}
	if status := runTypedTests(options, ""); status != "toolError" {
		t.Fatalf("oversized valid report status=%s", status)
	}
}

func TestLifecycleBridgeSignalHelper(t *testing.T) {
	if os.Getenv("SENTINEL_BRIDGE_SIGNAL_HELPER") != "1" {
		return
	}
	for index, argument := range os.Args {
		if argument == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[index+1:]...)
			main()
			return
		}
	}
}

func TestLifecycleBridgeSignalRestoresAndStopsCandidates(t *testing.T) {
	project := bridgeFixture(t, "package sample\nfunc Positive(x int) bool { return x > 0 }\n", "package sample\n")
	original, _ := os.ReadFile(filepath.Join(project, "value.go"))
	marker := filepath.Join(t.TempDir(), "pid")
	count := filepath.Join(t.TempDir(), "calls")
	runner := bridgeScript(t, fmt.Sprintf(`echo call >> %q
for arg in "$@"; do if [ "$arg" = '--coverprofile' ]; then coverage=1; fi; done
if [ "${coverage:-}" = 1 ]; then
  for last; do :; done
  printf 'mode: set\nexample.test/bridge/value.go:2.1,2.60 1 1\n' > "$last"
fi
if [ "$(wc -l < %q)" -gt 3 ]; then
  echo $$ > %q
  sleep 2
fi
printf '%%s' '%s'`, count, count, marker, strings.Replace(lifecycleTimedOut, "timedOut", "passed", 1)))
	goBinary := bridgeScript(t, "exit 0")
	command := exec.Command(os.Args[0], "-test.run=^TestLifecycleBridgeSignalHelper$", "--", "--project-root", project, "--go-binary", goBinary, "--runner-binary", runner, "--timeout-ms", "5000", "--source", "value.go")
	command.Env = append(os.Environ(), "SENTINEL_BRIDGE_SIGNAL_HELPER=1")
	var stdout bytes.Buffer
	command.Stdout = &stdout
	done := startBridgeSignalFixture(t, command, marker)
	waitBridgeFile(t, marker)
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("bridge shutdown unbounded")
	}
	after, _ := os.ReadFile(filepath.Join(project, "value.go"))
	if !bytes.Equal(original, after) {
		t.Error("mutated source not restored")
	}
	if stdout.Len() != 0 {
		t.Errorf("partial report=%q", stdout.String())
	}
	pid := bridgeFixturePID(t, marker)
	if !bridgeFixtureStopped(pid) {
		t.Error("typed runner still alive before fallback cleanup")
	}
	calls, _ := os.ReadFile(count)
	if strings.Count(string(calls), "call") != 4 {
		t.Errorf("later replay/candidate ran: %q", calls)
	}
}

func waitBridgeFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fixture did not start")
}

func bridgeFixturePID(t *testing.T, path string) int {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	if err != nil || pid < 1 {
		t.Fatalf("invalid fixture pid %q: %v", payload, err)
	}
	return pid
}

func bridgeFixtureStopped(pid int) bool {
	deadline := time.NewTimer(200 * time.Millisecond)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) || (err == nil && strings.Contains(string(state), ") Z ")) {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}

func startBridgeSignalFixture(t *testing.T, command *exec.Cmd, marker string) <-chan struct{} {
	t.Helper()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = time.Second
	done := make(chan struct{})
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	// Register before any fixture polling or assertion can exit the test.
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("fixture was not reaped after fallback")
				}
			}
		}
		cleanupBridgeFixturePID(t, marker)
	})
	go func() { _ = command.Wait(); close(done) }()
	return done
}

func cleanupBridgeFixturePID(t *testing.T, marker string) {
	t.Helper()
	payload, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Error(err)
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	if err != nil || pid < 1 {
		t.Errorf("invalid fixture PID %q", payload)
		return
	}
	if !bridgeFixtureStopped(pid) {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Error(err)
		}
	}
}

func TestLifecycleBridgeFixtureCleanupOnEarlyReturn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pid")
	var command *exec.Cmd
	var done <-chan struct{}
	t.Run("leave while helper runs", func(t *testing.T) {
		command = exec.Command("/usr/bin/bash", "-c", "echo $$ > \"$1\"; exec /usr/bin/sleep 2", "fixture", marker)
		done = startBridgeSignalFixture(t, command, marker)
		waitBridgeFile(t, marker)
	})
	select {
	case <-done:
	default:
		t.Fatal("early return did not wait/reap helper")
	}
	if command.ProcessState == nil || !bridgeFixtureStopped(command.Process.Pid) {
		t.Fatal("early return left helper running")
	}
}

func TestLifecycleCanceledBoundariesStartNoCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	marker := filepath.Join(t.TempDir(), "started")
	script := bridgeScript(t, fmt.Sprintf("touch %q", marker))
	options := bridgeOptions{projectRoot: t.TempDir(), goBinary: script, runnerBinary: script, timeout: time.Second}
	if status := runTypedTestsContext(ctx, options, ""); status != "toolError" {
		t.Errorf("typed status=%s", status)
	}
	if status := runGo(ctx, options, "test"); status != "toolError" {
		t.Errorf("compile status=%s", status)
	}
	if outcomes, err := executePlan(ctx, options, []plannedCandidate{{covered: false}, {covered: false}}); !errors.Is(err, context.Canceled) || outcomes != nil {
		t.Errorf("outcomes=%v err=%v", outcomes, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("command started: %v", err)
	}
}

func TestLifecycleRawCompileCancellationRestoresBeforeReturn(t *testing.T) {
	project := bridgeFixture(t, "package sample\n", "package sample\n")
	path := filepath.Join(project, "value.go")
	marker := filepath.Join(t.TempDir(), "compile")
	goBinary := bridgeScript(t, fmt.Sprintf("echo $$ > %q\nsleep 2", marker))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options := bridgeOptions{projectRoot: project, goBinary: goBinary, timeout: time.Second}
	done := make(chan error, 1)
	go func() {
		_, err := executeAndRestore(ctx, options, path, []byte("package sample\n"), []byte("package mutant\n"))
		done <- err
	}()
	waitBridgeFile(t, marker)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("error=%v", err)
	}
	payload, err := os.ReadFile(path)
	if err != nil || string(payload) != "package sample\n" {
		t.Errorf("source=%q err=%v", payload, err)
	}
	pid := bridgeFixturePID(t, marker)
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("compile process not reaped: %v", err)
	}
}

func TestLifecycleRestorationFailureWinsCancellation(t *testing.T) {
	project := bridgeFixture(t, "package sample\n", "package sample\n")
	path := filepath.Join(project, "value.go")
	marker := filepath.Join(t.TempDir(), "ready")
	goBinary := bridgeScript(t, fmt.Sprintf("mv value.go saved.go\nmkdir value.go\ntouch %q\nsleep 2", marker))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options := bridgeOptions{projectRoot: project, goBinary: goBinary, timeout: time.Second}
	done := make(chan error, 1)
	go func() {
		_, err := executeAndRestore(ctx, options, path, []byte("package sample\n"), []byte("package mutant\n"))
		done <- err
	}()
	waitBridgeFile(t, marker)
	cancel()
	if err := <-done; err == nil || errors.Is(err, context.Canceled) {
		t.Errorf("restoration failure hidden: %v", err)
	}
}

func TestLifecycleReplayDisagreementAndMalformedReports(t *testing.T) {
	count := filepath.Join(t.TempDir(), "count")
	passed := strings.Replace(lifecycleTimedOut, "timedOut", "passed", 1)
	failed := strings.Replace(lifecycleTimedOut, "timedOut", "assertionFailure", 1)
	script := bridgeScript(t, fmt.Sprintf("if [ -e %q ]; then printf '%%s' '%s'; else touch %q; printf '%%s' '%s'; fi", count, failed, count, passed))
	options := bridgeOptions{projectRoot: t.TempDir(), runnerBinary: script, goBinary: bridgeScript(t, "exit 0"), timeout: time.Second}
	if status := classifyMutant(context.Background(), options); status != "toolError" {
		t.Errorf("replay disagreement=%s", status)
	}
	for _, payload := range []string{"", "{}", lifecycleTimedOut + " trailing", strings.Replace(lifecycleTimedOut, "timedOut", "killed", 1), strings.Replace(lifecycleTimedOut, `"inventoryCount":1`, `"inventoryCount":0`, 1)} {
		if _, err := decodeTypedRunnerReport([]byte(payload)); err == nil {
			t.Errorf("malformed report accepted: %q", payload)
		}
	}
}

func TestLifecycleBridgeSignalReclaimsRealRunnerTestGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "test-pid")
	tests := fmt.Sprintf("package sample\nimport (\"testing\"; \"os\"; \"strconv\"; \"time\")\nfunc TestPositive(t *testing.T) { if Positive(0) { os.WriteFile(%q, []byte(strconv.Itoa(os.Getpid())), 0600); time.Sleep(3*time.Second); t.Fail() }; if !Positive(1) { t.Fail() } }\n", marker)
	project := bridgeFixture(t, "package sample\nfunc Positive(x int) bool { return x > 0 }\n", tests)
	original, _ := os.ReadFile(filepath.Join(project, "value.go"))
	command := exec.Command(os.Args[0], "-test.run=^TestLifecycleBridgeSignalHelper$", "--", "--project-root", project, "--go-binary", filepath.Join(runtime.GOROOT(), "bin", "go"), "--runner-binary", buildRunner(t), "--timeout-ms", "10000", "--source", "value.go")
	command.Env = append(os.Environ(), "SENTINEL_BRIDGE_SIGNAL_HELPER=1")
	var stdout bytes.Buffer
	command.Stdout = &stdout
	done := startBridgeSignalFixture(t, command, marker)
	waitBridgeFile(t, marker)
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("nested shutdown unbounded")
	}
	pid := bridgeFixturePID(t, marker)
	if !bridgeFixtureStopped(pid) {
		t.Error("real Go-test process alive before fallback cleanup")
	}
	after, _ := os.ReadFile(filepath.Join(project, "value.go"))
	afterTests, _ := os.ReadFile(filepath.Join(project, "value_test.go"))
	if !bytes.Equal(original, after) || string(afterTests) != tests {
		t.Error("nested source restoration failed")
	}
	if stdout.Len() != 0 {
		t.Errorf("partial report=%q", stdout.String())
	}
}
