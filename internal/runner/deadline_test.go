package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestExecuteTimeoutReturnsTypedReplayAndRestoresSource(t *testing.T) {
	root, testPath, original := runnerSourceFixture(t, "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) {}\n")
	goBinary := fakeRunnerGo(t, "sleep 1\n")
	wantInventory := ""
	nonces := make(map[string]struct{})
	started := time.Now()
	for attempt := 0; attempt < 3; attempt++ {
		report, err := Execute(context.Background(), Request{
			ProjectRoot: root, GoBinary: goBinary, Timeout: 50 * time.Millisecond, CaptureReplay: true,
		})
		if err != nil {
			t.Fatalf("attempt %d returned collector error: %v", attempt, err)
		}
		if report.Status != StatusTimedOut || report.Replay == nil || report.InventoryCount != 1 {
			t.Fatalf("attempt %d report=%#v", attempt, report)
		}
		if report.Nonce == "" {
			t.Fatalf("attempt %d returned an empty nonce", attempt)
		}
		if _, repeated := nonces[report.Nonce]; repeated {
			t.Fatalf("attempt %d reused nonce %q", attempt, report.Nonce)
		}
		nonces[report.Nonce] = struct{}{}
		if wantInventory == "" {
			wantInventory = report.Replay.InventorySHA256
		} else if report.Replay.InventorySHA256 != wantInventory {
			t.Fatalf("attempt %d changed replay inventory", attempt)
		}
		after, readErr := os.ReadFile(testPath)
		if readErr != nil || !bytes.Equal(after, original) {
			t.Fatalf("attempt %d did not restore source: %v", attempt, readErr)
		}
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("typed timeout repetitions were not bounded: %s", elapsed)
	}
}

func TestRunGoTestDisablesImplicitGoTimeout(t *testing.T) {
	argumentsPath := filepath.Join(t.TempDir(), "arguments")
	goBinary := fakeRunnerGo(t, "printf '%s\\n' \"$@\" > "+strconv.Quote(argumentsPath)+"\n")
	_, runErr, timedOut, err := runGoTest(context.Background(), Request{
		ProjectRoot: t.TempDir(), GoBinary: goBinary, Timeout: time.Second,
	}, []string{"PATH=/usr/bin:/bin"})
	if err != nil || runErr != nil || timedOut {
		t.Fatalf("runErr=%v timedOut=%v err=%v", runErr, timedOut, err)
	}
	payload, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"test", "./...", "-count=1", "-json", "-timeout=0"}
	if got := strings.Fields(string(payload)); !equalStrings(got, want) {
		t.Fatalf("go arguments=%q want=%q", got, want)
	}
}

func TestExecuteCallerCancellationDoesNotReturnReport(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		newContext func() (context.Context, context.CancelFunc)
		want       error
	}{
		{
			name: "cancelled",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			want: context.Canceled,
		},
		{
			name: "expired",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 50*time.Millisecond)
			},
			want: context.DeadlineExceeded,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root, testPath, original := runnerSourceFixture(t, "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) {}\n")
			startedPath := filepath.Join(t.TempDir(), "started")
			goBinary := fakeRunnerGo(t, "printf started > "+strconv.Quote(startedPath)+"\nsleep 2\n")
			ctx, cancel := testCase.newContext()
			defer cancel()
			if testCase.want == context.Canceled {
				go func() {
					waitForPath(startedPath, time.Second)
					cancel()
				}()
			}
			started := time.Now()
			report, err := Execute(ctx, Request{
				ProjectRoot: root, GoBinary: goBinary, Timeout: 2 * time.Second, CaptureReplay: true,
			})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("report=%#v error=%v want=%v", report, err, testCase.want)
			}
			if report != (Report{}) {
				t.Fatalf("caller cancellation manufactured report %#v", report)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("caller cancellation was not bounded: %s", elapsed)
			}
			after, readErr := os.ReadFile(testPath)
			if readErr != nil || !bytes.Equal(after, original) {
				t.Fatalf("caller cancellation did not restore source: %v", readErr)
			}
		})
	}
}

func TestExecuteCancellationImmediatelyAfterDrainCheckDoesNotReturnReport(t *testing.T) {
	root, testPath, original := runnerSourceFixture(t, "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) {}\n")
	underlying, cancel := context.WithCancel(context.Background())
	defer cancel()
	parent := &cancelImmediatelyAfterSecondErr{Context: underlying, cancel: cancel}
	report, err := Execute(parent, Request{
		ProjectRoot: root, GoBinary: fakeRunnerGo(t, "exit 0\n"), Timeout: time.Second, CaptureReplay: true,
	})
	if !errors.Is(underlying.Err(), context.Canceled) {
		t.Fatalf("fixture did not cancel its underlying context: %v", underlying.Err())
	}
	if !errors.Is(err, context.Canceled) || report != (Report{}) {
		t.Fatalf("late caller cancellation returned report=%#v error=%v", report, err)
	}
	after, readErr := os.ReadFile(testPath)
	if readErr != nil || !bytes.Equal(after, original) {
		t.Fatalf("late caller cancellation did not restore source: %v", readErr)
	}
}

type cancelImmediatelyAfterSecondErr struct {
	context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	calls  int
}

func (parent *cancelImmediatelyAfterSecondErr) Err() error {
	parent.mu.Lock()
	defer parent.mu.Unlock()
	parent.calls++
	err := parent.Context.Err()
	if parent.calls == 2 && err == nil {
		parent.cancel()
	}
	return err
}

func TestExecuteCancellationWhileDrainingDoesNotReturnReport(t *testing.T) {
	root, testPath, original := runnerSourceFixture(t, "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) {}\n")
	fixtureRoot := t.TempDir()
	mainPIDPath := filepath.Join(fixtureRoot, "main-pid")
	writerPIDPath := filepath.Join(fixtureRoot, "writer-pid")
	writerOpenedPath := filepath.Join(fixtureRoot, "writer-opened")
	commandDonePath := filepath.Join(fixtureRoot, "command-done")
	detached := `printf %s "$$" > "$1"; exec 3>"$2"; printf opened > "$3"; sleep 5`
	script := "printf %s \"$$\" > " + strconv.Quote(mainPIDPath) + "\n" +
		"/usr/bin/setsid /bin/sh -c '" + detached + "' retained-writer " + strconv.Quote(writerPIDPath) +
		" \"$SENTINEL_GO_RUNNER_EVENTS\" " + strconv.Quote(writerOpenedPath) + " </dev/null >/dev/null 2>&1 &\n" +
		"while [ ! -s " + strconv.Quote(writerOpenedPath) + " ]; do sleep 0.01; done\n" +
		"printf done > " + strconv.Quote(commandDonePath) + "\n"
	writerPID := 0
	t.Cleanup(func() {
		if writerPID == 0 {
			writerPID = readPID(writerPIDPath)
		}
		if writerPID > 1 {
			stopTestProcessGroup(t, writerPID)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	goBinary := fakeRunnerGo(t, script)
	type executeResult struct {
		report Report
		err    error
	}
	result := make(chan executeResult, 1)
	go func() {
		report, err := Execute(ctx, Request{
			ProjectRoot: root, GoBinary: goBinary, Timeout: 2 * time.Second, CaptureReplay: true,
		})
		result <- executeResult{report: report, err: err}
	}()
	if !waitForPathResult(commandDonePath, time.Second) {
		cancel()
		t.Fatal("fake Go command did not finish")
	}
	mainPID := readPID(mainPIDPath)
	if mainPID <= 1 || !waitForProcessExit(mainPID, time.Second) {
		cancel()
		t.Fatalf("fake Go process %d was not reaped", mainPID)
	}
	writerPID = readPID(writerPIDPath)
	if writerPID <= 1 {
		cancel()
		t.Fatalf("invalid retained writer pid %d", writerPID)
	}
	cancel()
	select {
	case got := <-result:
		if !errors.Is(got.err, context.Canceled) || got.report != (Report{}) {
			t.Fatalf("drain cancellation returned report=%#v error=%v", got.report, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("drain cancellation did not return")
	}
	after, readErr := os.ReadFile(testPath)
	if readErr != nil || !bytes.Equal(after, original) {
		t.Fatalf("drain cancellation did not restore source: %v", readErr)
	}
}

func TestExecuteRestoreErrorPrecedesCallerCancellation(t *testing.T) {
	root, testPath, _ := runnerSourceFixture(t, "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) {}\n")
	changedPath := filepath.Join(t.TempDir(), "changed")
	goBinary := fakeRunnerGo(t, "printf 'concurrent edit' > "+strconv.Quote(testPath)+"\n"+
		"printf changed > "+strconv.Quote(changedPath)+"\nsleep 2\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		waitForPath(changedPath, time.Second)
		cancel()
	}()
	report, err := Execute(ctx, Request{
		ProjectRoot: root, GoBinary: goBinary, Timeout: 2 * time.Second, CaptureReplay: true,
	})
	if err == nil || !strings.Contains(err.Error(), "verify or restore test sources") || report != (Report{}) {
		t.Fatalf("restore error lost to cancellation: report=%#v error=%v", report, err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("restore error was replaced or joined with cancellation: %v", err)
	}
	after, readErr := os.ReadFile(testPath)
	if readErr != nil || string(after) != "concurrent edit" {
		t.Fatalf("restore overwrote concurrent edit: payload=%q error=%v", after, readErr)
	}
}

func TestExecuteOutputOverflowPrecedesTimeout(t *testing.T) {
	root, testPath, original := runnerSourceFixture(t, "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) {}\n")
	fixtureRoot := t.TempDir()
	overflowCompletePath := filepath.Join(fixtureRoot, "overflow-complete")
	overflowCopyPath := filepath.Join(fixtureRoot, "overflow-copy")
	overflowStatusPath := filepath.Join(fixtureRoot, "overflow-status")
	mainPIDPath := filepath.Join(fixtureRoot, "main-pid")
	descendantPIDPath := filepath.Join(fixtureRoot, "descendant-pid")
	script := "printf %s \"$$\" > " + strconv.Quote(mainPIDPath) + "\n" +
		"/usr/bin/head -c " + strconv.Itoa(runnerOutputLimit+4096) + " /dev/zero | /usr/bin/tr '\\000' ' ' | /usr/bin/tee " + strconv.Quote(overflowCopyPath) + "\n" +
		"printf %s \"$?\" > " + strconv.Quote(overflowStatusPath) + "\n" +
		"printf '\\n'\n" +
		"printf complete > " + strconv.Quote(overflowCompletePath) + "\n" +
		"sleep 5 &\n" +
		"printf %s \"$!\" > " + strconv.Quote(descendantPIDPath) + "\n" +
		"wait\n"
	mainPID := 0
	t.Cleanup(func() {
		if mainPID == 0 {
			mainPID = readPID(mainPIDPath)
		}
		if mainPID > 1 {
			stopTestProcessGroup(t, mainPID)
		}
	})
	started := time.Now()
	report, err := Execute(context.Background(), Request{
		ProjectRoot: root, GoBinary: fakeRunnerGo(t, script), Timeout: 2 * time.Second, CaptureReplay: true,
	})
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("overflow timeout was not bounded: %s", elapsed)
	}
	if !waitForPathResult(overflowCompletePath, time.Millisecond) {
		t.Fatal("fake Go command did not complete oversized output")
	}
	overflowCopy, statErr := os.Stat(overflowCopyPath)
	if statErr != nil {
		t.Fatalf("inspect fake Go output copy: %v", statErr)
	}
	if overflowCopy.Size() <= int64(runnerOutputLimit) {
		t.Fatalf("fake Go output did not exceed limit: size=%d", overflowCopy.Size())
	}
	overflowStatus, statusErr := os.ReadFile(overflowStatusPath)
	if statusErr != nil || string(overflowStatus) != "0" {
		t.Fatalf("fake Go stdout write did not complete: status=%q error=%v", overflowStatus, statusErr)
	}
	if err == nil || err.Error() != "go test output exceeded limit" || report != (Report{}) {
		t.Fatalf("overflow plus timeout returned report=%#v error=%v", report, err)
	}
	mainPID = readPID(mainPIDPath)
	descendantPID := readPID(descendantPIDPath)
	if mainPID <= 1 || descendantPID <= 1 {
		t.Fatalf("invalid process ids main=%d descendant=%d", mainPID, descendantPID)
	}
	if !waitForProcessExit(mainPID, time.Second) || !waitForProcessExit(descendantPID, time.Second) {
		t.Fatalf("overflow timeout left process alive main=%d descendant=%d", mainPID, descendantPID)
	}
	after, readErr := os.ReadFile(testPath)
	if readErr != nil || !bytes.Equal(after, original) {
		t.Fatalf("overflow timeout did not restore source: %v", readErr)
	}
}

func TestExecuteOutputOverflowOnNormalExitIsAnError(t *testing.T) {
	root, testPath, original := runnerSourceFixture(t, "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) {}\n")
	fixtureRoot := t.TempDir()
	overflowCopyPath := filepath.Join(fixtureRoot, "overflow-copy")
	overflowStatusPath := filepath.Join(fixtureRoot, "overflow-status")
	script := "/usr/bin/head -c " + strconv.Itoa(runnerOutputLimit+4096) + " /dev/zero | /usr/bin/tr '\\000' ' ' | /usr/bin/tee " + strconv.Quote(overflowCopyPath) + "\n" +
		"printf %s \"$?\" > " + strconv.Quote(overflowStatusPath) + "\n" +
		"printf '\\n'\n"
	report, err := Execute(context.Background(), Request{
		ProjectRoot: root, GoBinary: fakeRunnerGo(t, script), Timeout: 5 * time.Second, CaptureReplay: true,
	})
	overflowCopy, statErr := os.Stat(overflowCopyPath)
	if statErr != nil || overflowCopy.Size() <= int64(runnerOutputLimit) {
		t.Fatalf("fake Go output did not exceed limit: info=%v error=%v", overflowCopy, statErr)
	}
	overflowStatus, statusErr := os.ReadFile(overflowStatusPath)
	if statusErr != nil || string(overflowStatus) != "0" {
		t.Fatalf("fake Go stdout write did not complete: status=%q error=%v", overflowStatus, statusErr)
	}
	if err == nil || err.Error() != "go test output exceeded limit" || report != (Report{}) {
		t.Fatalf("normal overflow returned report=%#v error=%v", report, err)
	}
	after, readErr := os.ReadFile(testPath)
	if readErr != nil || !bytes.Equal(after, original) {
		t.Fatalf("normal overflow did not restore source: %v", readErr)
	}
}

func TestExecuteRetainedEventWriterUsesBoundedDrain(t *testing.T) {
	root, testPath, original := runnerSourceFixture(t, "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) {}\n")
	pidPath := filepath.Join(t.TempDir(), "retained-writer-pid")
	detached := `printf %s "$$" > "$1"; exec 3>"$2"; sleep 5`
	script := "/usr/bin/setsid /bin/sh -c '" + detached + "' retained-writer " + strconv.Quote(pidPath) +
		" \"$SENTINEL_GO_RUNNER_EVENTS\" </dev/null >/dev/null 2>&1 &\n" +
		"while [ ! -s " + strconv.Quote(pidPath) + " ]; do sleep 0.01; done\n" +
		"sleep 5\n"
	goBinary := fakeRunnerGo(t, script)
	retainedPID := 0
	t.Cleanup(func() {
		if retainedPID == 0 {
			retainedPID = readPID(pidPath)
		}
		if retainedPID > 1 {
			stopTestProcessGroup(t, retainedPID)
		}
	})
	started := time.Now()
	report, err := Execute(context.Background(), Request{
		ProjectRoot: root, GoBinary: goBinary, Timeout: 50 * time.Millisecond, CaptureReplay: true,
	})
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) || report != (Report{}) {
		t.Fatalf("report=%#v error=%v", report, err)
	}
	if elapsed < 900*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("retained writer drain=%s, want about one second", elapsed)
	}
	retainedPID = readPID(pidPath)
	if retainedPID <= 1 {
		t.Fatalf("invalid retained writer pid %d", retainedPID)
	}
	if processGroup, groupErr := syscall.Getpgid(retainedPID); groupErr != nil || processGroup != retainedPID {
		t.Fatalf("retained writer pid=%d processGroup=%d error=%v", retainedPID, processGroup, groupErr)
	}
	after, readErr := os.ReadFile(testPath)
	if readErr != nil || !bytes.Equal(after, original) {
		t.Fatalf("retained writer error did not restore source: %v", readErr)
	}
}

func readPID(path string) int {
	payload, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	if err != nil {
		return 0
	}
	return pid
}

func stopTestProcessGroup(t *testing.T, processGroup int) {
	t.Helper()
	if err := syscall.Kill(-processGroup, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill retained process group %d: %v", processGroup, err)
		return
	}
	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(-processGroup, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Errorf("inspect retained process group %d: %v", processGroup, err)
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("retained process group %d survived cleanup", processGroup)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestExecuteMalformedPrivateEventsRemainErrors(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		payload string
	}{
		{name: "truncated-prefix", payload: `x`},
		{name: "truncated-payload", payload: `\000\000\000\002{`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root, testPath, original := runnerSourceFixture(t, "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) {}\n")
			goBinary := fakeRunnerGo(t, "printf '"+testCase.payload+"' > \"$SENTINEL_GO_RUNNER_EVENTS\"\n")
			report, err := Execute(context.Background(), Request{
				ProjectRoot: root, GoBinary: goBinary, Timeout: time.Second, CaptureReplay: true,
			})
			if err == nil || report != (Report{}) {
				t.Fatalf("malformed private event returned report=%#v error=%v", report, err)
			}
			after, readErr := os.ReadFile(testPath)
			if readErr != nil || !bytes.Equal(after, original) {
				t.Fatalf("malformed private event did not restore source: %v", readErr)
			}
		})
	}
}

func fakeRunnerGo(t *testing.T, body string) string {
	t.Helper()
	toolchainRoot := filepath.Join(t.TempDir(), "toolchain")
	installRoot := filepath.Join(toolchainRoot, "go-1.27.1")
	goBinary := filepath.Join(installRoot, "bin", "go")
	if err := os.MkdirAll(filepath.Dir(goBinary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goBinary, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home", "config", "tmp", "gopath", "build-cache", "module-cache"} {
		if err := os.MkdirAll(filepath.Join(toolchainRoot, "state", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return goBinary
}

func waitForPath(path string, timeout time.Duration) {
	_ = waitForPathResult(path, timeout)
}

func waitForPathResult(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
