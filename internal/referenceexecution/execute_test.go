package referenceexecution

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/hwain-hwang/sentinel-go/internal/runner"
	"github.com/hwain-hwang/sentinel-go/internal/workspace"
)

const validRequestID = "0123456789abcdef0123456789abcdef"

func TestExecuteValidatesLexicallyBeforeCreatingGuard(t *testing.T) {
	valid := Request{ProjectRoot: "relative-project", GoBinary: "relative-go", RequestID: validRequestID, Timeout: time.Millisecond}
	cases := []struct {
		name    string
		ctx     context.Context
		request Request
	}{
		{name: "nil-context", request: valid},
		{name: "empty-project", ctx: context.Background(), request: replaceRequest(valid, func(request *Request) { request.ProjectRoot = "" })},
		{name: "empty-go", ctx: context.Background(), request: replaceRequest(valid, func(request *Request) { request.GoBinary = "" })},
		{name: "short-id", ctx: context.Background(), request: replaceRequest(valid, func(request *Request) { request.RequestID = validRequestID[:31] })},
		{name: "uppercase-id", ctx: context.Background(), request: replaceRequest(valid, func(request *Request) { request.RequestID = "A123456789abcdef0123456789abcdef" })},
		{name: "non-hex-id", ctx: context.Background(), request: replaceRequest(valid, func(request *Request) { request.RequestID = "g123456789abcdef0123456789abcdef" })},
		{name: "sub-millisecond", ctx: context.Background(), request: replaceRequest(valid, func(request *Request) { request.Timeout = time.Nanosecond })},
		{name: "fractional-millisecond", ctx: context.Background(), request: replaceRequest(valid, func(request *Request) { request.Timeout = time.Millisecond + time.Nanosecond })},
		{name: "over-limit", ctx: context.Background(), request: replaceRequest(valid, func(request *Request) { request.Timeout = 24*time.Hour + time.Millisecond })},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			created := false
			got, err := executeWith(testCase.ctx, testCase.request, dependencies{
				create: func(string) (projectGuard, error) { created = true; return &fakeGuard{}, nil },
				run: func(context.Context, runner.Request) (runner.Report, error) {
					t.Fatal("runner called")
					return runner.Report{}, nil
				},
			})
			if !errors.Is(err, errInvalidRequest) || got != (Receipt{}) || created {
				t.Fatalf("receipt=%#v error=%v created=%v", got, err, created)
			}
		})
	}
}

func TestExecuteAcceptsExactTimeoutBoundariesAndRelativePaths(t *testing.T) {
	for _, timeout := range []time.Duration{time.Millisecond, 24 * time.Hour} {
		guard := &fakeGuard{digest: strings.Repeat("a", 64)}
		request := Request{ProjectRoot: "relative-project", GoBinary: "relative-go", RequestID: validRequestID, Timeout: timeout}
		got, err := executeWith(context.Background(), request, dependencies{
			create: func(root string) (projectGuard, error) {
				if root != request.ProjectRoot {
					t.Fatalf("root=%q", root)
				}
				return guard, nil
			},
			run: func(_ context.Context, got runner.Request) (runner.Report, error) {
				if got.ProjectRoot != request.ProjectRoot || got.GoBinary != request.GoBinary || got.Timeout != timeout || !got.CaptureReplay || got.CoverProfile != "" {
					t.Fatalf("runner request=%#v", got)
				}
				return runner.Report{SchemaVersion: runner.RunnerSchema, Nonce: "nonce", Status: runner.StatusPassed, InventoryCount: 1,
					Replay: &runner.ReplayIdentity{InventorySHA256: "inventory", ResultsSHA256: "results"}}, nil
			},
		})
		if err != nil || got.TimeoutMilliseconds != timeout.Milliseconds() || got.Replay == nil || got.Replay.FailureSHA256 != "" {
			t.Fatalf("receipt=%#v error=%v", got, err)
		}
	}
}

func TestExecutePreCancelledContextDoesNotCreateGuard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	created := false
	got, err := executeWith(ctx, Request{ProjectRoot: "missing", GoBinary: "missing", RequestID: validRequestID, Timeout: time.Second}, dependencies{
		create: func(string) (projectGuard, error) { created = true; return &fakeGuard{}, nil },
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, errCancelled) || created || got != (Receipt{}) {
		t.Fatalf("receipt=%#v error=%v created=%v", got, err, created)
	}
}

func TestExecutePreservesSanitizedLifecycleFailuresAndAlwaysRemoves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	guard := &fakeGuard{
		digest:    strings.Repeat("b", 64),
		verifyErr: errors.New("raw verify /secret/project/value.go"),
		removeErr: errors.New("raw remove /tmp/secret-snapshot"),
	}
	got, err := executeWith(ctx, Request{ProjectRoot: "project", GoBinary: "go", RequestID: validRequestID, Timeout: time.Second}, dependencies{
		create: func(string) (projectGuard, error) { return guard, nil },
		run: func(context.Context, runner.Request) (runner.Report, error) {
			cancel()
			return runner.Report{}, errors.New("raw runner output SecretTest")
		},
	})
	if got != (Receipt{}) || !guard.removed {
		t.Fatalf("receipt=%#v removed=%v", got, guard.removed)
	}
	for _, target := range []error{errRunner, errInputChanged, errCleanup, errCancelled, context.Canceled} {
		if !errors.Is(err, target) {
			t.Errorf("missing category %v in %v", target, err)
		}
	}
	for _, secret := range []string{"SecretTest", "/secret/project", "/tmp/secret-snapshot"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaked %q: %v", secret, err)
		}
	}
}

func TestExecuteSanitizesGuardCreationFailure(t *testing.T) {
	got, err := executeWith(context.Background(), Request{ProjectRoot: "secret/project", GoBinary: "go", RequestID: validRequestID, Timeout: time.Second}, dependencies{
		create: func(string) (projectGuard, error) { return nil, errors.New("stat secret/project: denied") },
	})
	if got != (Receipt{}) || !errors.Is(err, errGuard) || errors.Is(err, errCancelled) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("receipt=%#v error=%v", got, err)
	}
}

func TestExecuteGuardCreationFailurePreservesConcurrentCancellation(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		got, err := executeWith(ctx, Request{ProjectRoot: "secret/project", GoBinary: "go", RequestID: validRequestID, Timeout: time.Second}, dependencies{
			create: func(string) (projectGuard, error) {
				cancel()
				return nil, errors.New("raw guard /secret/project")
			},
		})
		assertGuardCancellation(t, got, err, context.Canceled)
	})

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		got, err := executeWith(ctx, Request{ProjectRoot: "secret/project", GoBinary: "go", RequestID: validRequestID, Timeout: time.Second}, dependencies{
			create: func(string) (projectGuard, error) {
				<-ctx.Done()
				return nil, errors.New("raw guard /secret/project")
			},
		})
		assertGuardCancellation(t, got, err, context.DeadlineExceeded)
	})
}

func TestExecuteSkipsRunnerWhenCancelledAfterGuardAcquisition(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	guard := &fakeGuard{
		verifyErr: errors.New("raw verify /secret/project"),
		removeErr: errors.New("raw remove /secret/snapshot"),
	}
	runnerCalled := false
	got, err := executeWith(ctx, Request{ProjectRoot: "secret/project", GoBinary: "go", RequestID: validRequestID, Timeout: time.Second}, dependencies{
		create: func(string) (projectGuard, error) {
			cancel()
			return guard, nil
		},
		run: func(context.Context, runner.Request) (runner.Report, error) {
			runnerCalled = true
			return runner.Report{}, nil
		},
	})
	if got != (Receipt{}) || runnerCalled || !guard.verified || !guard.removed {
		t.Fatalf("receipt=%#v runnerCalled=%v verified=%v removed=%v", got, runnerCalled, guard.verified, guard.removed)
	}
	for _, target := range []error{errInputChanged, errCleanup, errCancelled, context.Canceled} {
		if !errors.Is(err, target) {
			t.Errorf("missing category %v in %v", target, err)
		}
	}
	if errors.Is(err, errRunner) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected runner category or raw detail: %v", err)
	}
}

func TestExecuteReusesWorkspaceAndRunnerIdentities(t *testing.T) {
	cases := []struct {
		name       string
		testSource string
		status     runner.Status
	}{
		{name: "passing", status: runner.StatusPassed, testSource: `package fixture
import "testing"
func TestSecretCustomer(t *testing.T) { if Value() != 1 { t.Fail() } }
`},
		{name: "assertion-failure", status: runner.StatusAssertionFailure, testSource: `package fixture
import "testing"
func TestSecretCustomer(t *testing.T) { t.Fatal("SecretFailureMessage") }
`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root, originals := referenceFixture(t, testCase.testSource)
			snapshot, err := workspace.Create(root)
			if err != nil {
				t.Fatal(err)
			}
			wantInput := snapshot.ContentSHA256()
			if err := snapshot.Remove(); err != nil {
				t.Fatal(err)
			}

			request := realRequest(root, 20*time.Second)
			first, err := Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			second, err := Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := runner.Execute(context.Background(), runner.Request{ProjectRoot: root, GoBinary: request.GoBinary, Timeout: request.Timeout, CaptureReplay: true})
			if err != nil {
				t.Fatal(err)
			}

			if first.Status != testCase.status || second.Status != testCase.status || raw.Status != testCase.status {
				t.Fatalf("statuses first=%q second=%q raw=%q", first.Status, second.Status, raw.Status)
			}
			if first.SchemaVersion != "sentinel-go-reference-execution-v1" || first.Profile != "replay-identity-v1" || first.RunnerSchema != runner.RunnerSchema || !first.ReferenceOnly || first.Certified {
				t.Fatalf("receipt contract=%#v", first)
			}
			if first.RequestID != validRequestID || first.Nonce == "" || first.Nonce == first.RequestID || first.Nonce == second.Nonce {
				t.Fatalf("request/nonce distinction first=%#v second nonce=%q", first, second.Nonce)
			}
			if first.InputSHA256 != wantInput || second.InputSHA256 != wantInput {
				t.Fatalf("input hashes first=%q second=%q want=%q", first.InputSHA256, second.InputSHA256, wantInput)
			}
			if first.Replay == nil || second.Replay == nil || raw.Replay == nil {
				t.Fatalf("missing replay first=%#v second=%#v raw=%#v", first, second, raw)
			}
			wantReplay := Replay{InventorySHA256: raw.Replay.InventorySHA256, ResultsSHA256: raw.Replay.ResultsSHA256, FailureSHA256: raw.Replay.FailureSHA256}
			if *first.Replay != wantReplay || *second.Replay != wantReplay {
				t.Fatalf("replay first=%#v second=%#v raw=%#v", first.Replay, second.Replay, raw.Replay)
			}
			assertFixtureUnchanged(t, root, originals)
			payload, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			assertExactReceiptJSON(t, payload)
			for _, secret := range []string{root, "SecretCustomer", "SecretFailureMessage", "example.test/private"} {
				if strings.Contains(string(payload), secret) {
					t.Errorf("receipt leaked %q: %s", secret, payload)
				}
			}
		})
	}
}

func TestExecuteKeepsUnsupportedMethodValueAsToolErrorWithoutReplay(t *testing.T) {
	root, _ := referenceFixture(t, `package fixture
import "testing"
func TestSecretMethodValue(t *testing.T) { fail := t.Fail; fail() }
`)
	request := realRequest(root, 20*time.Second)
	legacy, err := runner.Execute(context.Background(), runner.Request{ProjectRoot: root, GoBinary: request.GoBinary, Timeout: request.Timeout})
	if err != nil || legacy.Status != runner.StatusAssertionFailure {
		t.Fatalf("legacy observation=%#v error=%v", legacy, err)
	}
	receipt, err := Execute(context.Background(), request)
	if err != nil || receipt.Status != runner.StatusToolError || receipt.Replay != nil {
		t.Fatalf("reference observation=%#v error=%v", receipt, err)
	}
	legacy.Replay = &runner.ReplayIdentity{InventorySHA256: "must-not-serialize"}
	legacy.InputSHA256 = "must-not-serialize"
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "replay") || strings.Contains(string(payload), "inputSha256") || !strings.Contains(string(payload), `"schemaVersion":"sentinel-go-typed-runner-v1"`) {
		t.Fatalf("legacy v1 wire contract changed: %s", payload)
	}
}

func TestExecuteDoesNotReclassifyCompileErrorAsAssertionFailure(t *testing.T) {
	root, _ := referenceFixture(t, "package fixture\nimport \"testing\"\nfunc TestSecret(t *testing.T) {}\n")
	writeReferenceFile(t, filepath.Join(root, "broken.go"), "package fixture\nfunc Broken( {\n")
	receipt, err := Execute(context.Background(), realRequest(root, 20*time.Second))
	if err != nil || receipt.Status != runner.StatusCompileError || receipt.Status == runner.StatusAssertionFailure {
		t.Fatalf("receipt=%#v error=%v", receipt, err)
	}
}

func TestExecuteReturnsTimedOutReceiptAndCleansProcessGroup(t *testing.T) {
	root, _ := processFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	registerProcessFixtureFallback(t, root, cancel, nil)
	receipt, err := Execute(ctx, realRequest(root, 3*time.Second))
	if err != nil || receipt.Status != runner.StatusTimedOut || receipt.Status == runner.StatusAssertionFailure {
		t.Fatalf("receipt=%#v error=%v", receipt, err)
	}
	pid := readFixturePID(t, root)
	assertProcessGone(t, pid)
}

func TestExecuteParentCancellationReturnsNoReceiptAndCleansProcessGroup(t *testing.T) {
	root, _ := processFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan struct {
		receipt Receipt
		err     error
	}, 1)
	executionDone := make(chan struct{})
	registerProcessFixtureFallback(t, root, cancel, executionDone)
	go func() {
		defer close(executionDone)
		receipt, err := Execute(ctx, realRequest(root, 20*time.Second))
		result <- struct {
			receipt Receipt
			err     error
		}{receipt: receipt, err: err}
	}()
	pid := waitFixturePID(t, root, 10*time.Second)
	cancel()
	select {
	case got := <-result:
		if got.receipt != (Receipt{}) || !errors.Is(got.err, context.Canceled) || !errors.Is(got.err, errCancelled) {
			t.Fatalf("receipt=%#v error=%v", got.receipt, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled execution did not return")
	}
	waitFixtureExecution(t, executionDone)
	assertProcessGone(t, pid)
}

func TestProcessFixtureFallbackCancelsJoinsAndKillsOwnedChild(t *testing.T) {
	root := t.TempDir()
	markerDirectory := filepath.Join(root, ".sentinel")
	if err := os.MkdirAll(markerDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(reaped)
	}()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		select {
		case <-reaped:
		case <-time.After(2 * time.Second):
			t.Errorf("owned helper process was not reaped")
		}
	})
	writeReferenceFile(t, filepath.Join(markerDirectory, "child.pid"), strconv.Itoa(command.Process.Pid))

	cancelled := make(chan struct{})
	executionDone := make(chan struct{})
	cancel := func() {
		select {
		case <-cancelled:
		default:
			close(cancelled)
		}
	}
	go func() {
		<-cancelled
		close(executionDone)
	}()

	cleanupProcessFixture(t, root, cancel, executionDone)
	select {
	case <-executionDone:
	case <-time.After(time.Second):
		t.Fatal("fixture fallback did not cancel and join execution")
	}
	select {
	case <-reaped:
	case <-time.After(time.Second):
		t.Fatal("fixture fallback did not kill the exact owned child")
	}
	assertProcessGone(t, command.Process.Pid)
}

func TestExecuteDetectsActualSourceMutationAndClearsReceipt(t *testing.T) {
	root, _ := referenceFixture(t, `package fixture
import (
	"os"
	"testing"
)
func TestSecretMutation(t *testing.T) {
	if err := os.WriteFile("value.go", []byte("package fixture\nfunc Value() int { return 2 }\n"), 0600); err != nil { t.Fatal(err) }
}
`)
	receipt, err := Execute(context.Background(), realRequest(root, 20*time.Second))
	if receipt != (Receipt{}) || !errors.Is(err, errInputChanged) || strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "value.go") {
		t.Fatalf("receipt=%#v error=%v", receipt, err)
	}
}

type fakeGuard struct {
	digest    string
	verifyErr error
	removeErr error
	verified  bool
	removed   bool
}

func (guard *fakeGuard) ContentSHA256() string { return guard.digest }
func (guard *fakeGuard) VerifyOriginal() error { guard.verified = true; return guard.verifyErr }
func (guard *fakeGuard) Remove() error         { guard.removed = true; return guard.removeErr }

func assertGuardCancellation(t *testing.T, got Receipt, err, cause error) {
	t.Helper()
	if got != (Receipt{}) || !errors.Is(err, errGuard) || !errors.Is(err, errCancelled) || !errors.Is(err, cause) {
		t.Fatalf("receipt=%#v error=%v", got, err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("raw guard detail leaked: %v", err)
	}
}

func replaceRequest(request Request, change func(*Request)) Request {
	change(&request)
	return request
}

func realRequest(root string, timeout time.Duration) Request {
	return Request{ProjectRoot: root, GoBinary: filepath.Join(runtime.GOROOT(), "bin", "go"), RequestID: validRequestID, Timeout: timeout}
}

func referenceFixture(t *testing.T, testSource string) (string, map[string][]byte) {
	t.Helper()
	root := t.TempDir()
	files := map[string][]byte{
		"go.mod":        []byte("module example.test/private\n\ngo 1.27.0\n"),
		"value.go":      []byte("package fixture\nfunc Value() int { return 1 }\n"),
		"value_test.go": []byte(testSource),
	}
	for name, payload := range files {
		writeReferenceFile(t, filepath.Join(root, name), string(payload))
	}
	return root, files
}

func processFixture(t *testing.T) (string, map[string][]byte) {
	t.Helper()
	return referenceFixture(t, `package fixture
import (
	"os"
	"os/exec"
	"testing"
)
func TestWaitForCancellation(t *testing.T) {
	if err := os.MkdirAll(".sentinel", 0700); err != nil { t.Fatal(err) }
	command := exec.Command("/bin/sh", "-c", "echo $$ > .sentinel/child.pid; exec sleep 30")
	if err := command.Run(); err != nil { t.Fatal(err) }
}
`)
}

func writeReferenceFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFixtureUnchanged(t *testing.T, root string, originals map[string][]byte) {
	t.Helper()
	for name, want := range originals {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s changed: error=%v", name, err)
		}
	}
}

func assertExactReceiptJSON(t *testing.T, payload []byte) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	want := []string{"schemaVersion", "profile", "requestId", "timeoutMilliseconds", "runnerSchema", "nonce", "status", "inventoryCount", "inputSha256", "replay", "referenceOnly", "certified"}
	if len(object) != len(want) {
		t.Fatalf("JSON fields=%v", object)
	}
	for _, key := range want {
		if _, exists := object[key]; !exists {
			t.Errorf("missing JSON field %q", key)
		}
	}
	if string(object["replay"]) != "null" {
		var replay map[string]json.RawMessage
		if err := json.Unmarshal(object["replay"], &replay); err != nil {
			t.Fatal(err)
		}
		if len(replay) != 3 || replay["inventorySha256"] == nil || replay["resultsSha256"] == nil || replay["failureSha256"] == nil {
			t.Fatalf("replay JSON fields=%v", replay)
		}
	}
}

func waitFixturePID(t *testing.T, root string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		payload, err := os.ReadFile(filepath.Join(root, ".sentinel", "child.pid"))
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(payload)))
			if parseErr != nil || pid < 1 {
				t.Fatalf("invalid fixture pid %q", payload)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fixture child did not start")
	return 0
}

func readFixturePID(t *testing.T, root string) int {
	t.Helper()
	return waitFixturePID(t, root, time.Second)
}

func registerProcessFixtureFallback(t *testing.T, root string, cancel context.CancelFunc, executionDone <-chan struct{}) {
	t.Helper()
	t.Cleanup(func() { cleanupProcessFixture(t, root, cancel, executionDone) })
}

func cleanupProcessFixture(t *testing.T, root string, cancel context.CancelFunc, executionDone <-chan struct{}) {
	t.Helper()
	cancel()
	if executionDone != nil {
		select {
		case <-executionDone:
		case <-time.After(5 * time.Second):
			t.Errorf("fixture execution did not stop during fallback cleanup")
		}
	}
	payload, err := os.ReadFile(filepath.Join(root, ".sentinel", "child.pid"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Errorf("read owned fixture pid during cleanup: %v", err)
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	if err != nil || pid < 1 {
		t.Errorf("invalid owned fixture pid during cleanup: %q", payload)
		return
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill owned fixture process %d: %v", pid, err)
	}
}

func waitFixtureExecution(t *testing.T, executionDone <-chan struct{}) {
	t.Helper()
	select {
	case <-executionDone:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture execution goroutine did not stop")
	}
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("fixture process %d survived", pid))
}
