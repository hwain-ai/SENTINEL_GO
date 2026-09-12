package mutation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/workspace"
)

func lifecycleRequest(t *testing.T, body string) Request {
	t.Helper()
	backend := mutationTestBackend(t, body)
	return Request{ProjectRoot: mutationTestProject(t), Sources: []string{"value.go"}, BackendExecutable: backend, RunnerExecutable: backend, GoBinary: filepath.Join(runtimeGOROOT(t), "bin", "go"), Timeout: time.Second}
}

func TestAdapterLifecycleOutputOverflow(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		for _, ending := range []string{"exit 0", "/usr/bin/sleep 2"} {
			t.Run(stream+ending, func(t *testing.T) {
				body := "/usr/bin/head -c 16777217 /dev/zero"
				if stream == "stderr" {
					body += " >&2"
				}
				request := lifecycleRequest(t, body+"\n"+ending)
				request.Timeout = 250 * time.Millisecond
				before := mutationTestDigest(t, filepath.Join(request.ProjectRoot, "value.go"))
				report, err := NewAdapter().Run(context.Background(), request)
				if err == nil || err.Error() != "backendOutputTooLarge" || report.SchemaVersion != "" {
					t.Errorf("report=%+v err=%v", report, err)
				}
				if mutationTestDigest(t, filepath.Join(request.ProjectRoot, "value.go")) != before {
					t.Fatal("original changed")
				}
			})
		}
	}
}

func TestAdapterLifecycleStopsInheritedPipeChild(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelParent), func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pid")
			marker := filepath.Join(t.TempDir(), "late")
			request := lifecycleRequest(t, fmt.Sprintf("(/usr/bin/sleep .45; /usr/bin/touch %q; /usr/bin/sleep 2) &\necho $! > %q\nwait", marker, pidFile))
			request.Timeout = 150 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelParent {
				request.Timeout = time.Second
				timer := time.AfterFunc(150*time.Millisecond, cancel)
				defer timer.Stop()
			}
			before := mutationTestDigest(t, filepath.Join(request.ProjectRoot, "value.go"))
			start := time.Now()
			report, err := NewAdapter().Run(ctx, request)
			payload, readErr := os.ReadFile(pidFile)
			if readErr != nil {
				t.Fatal(readErr)
			}
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(payload)))
			if parseErr != nil || pid < 1 {
				t.Fatalf("invalid fixture pid %q: %v", payload, parseErr)
			}
			stopped := lifecycleStopped(pid)
			// Observe before the bounded fixture's fallback cleanup.
			if !stopped {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Error("exact inherited-pipe child still running")
			}
			if _, err := os.Stat(marker); err == nil {
				t.Error("delayed child marker exists")
			}
			if time.Since(start) > time.Second {
				t.Errorf("return took %v", time.Since(start))
			}
			if err == nil || report.SchemaVersion != "" {
				t.Errorf("report=%+v err=%v", report, err)
			}
			if mutationTestDigest(t, filepath.Join(request.ProjectRoot, "value.go")) != before {
				t.Fatal("original changed")
			}
		})
	}
}

func lifecycleStopped(pid int) bool {
	// SIGKILL delivery is asynchronous for descendants we cannot wait/reap.
	// Observe for at most 200 ms, without signaling the child during this window.
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		if lifecycleProcessStopped(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func lifecycleProcessStopped(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return true
	}
	payload, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err != nil || strings.Contains(string(payload), ") Z ")
}

// These deterministic helper checks exercise the synchronous return boundary;
// they do not claim atomic cancellation at an arbitrary public return instant.
func TestAdapterLifecycleFinalBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executionErr := &AdapterError{Code: "backendReportInvalid", ExitCode: 6}
	snapshot := &workspace.Snapshot{Root: filepath.Join(t.TempDir(), "not-a-snapshot")}
	cleanupErr := snapshot.Remove()
	if cleanupErr == nil {
		t.Fatal("unsafe removal should fail")
	}
	for _, test := range []struct {
		existing, cleanup error
		want              string
	}{
		{nil, nil, "backendProcessFailed"},
		{nil, cleanupErr, "snapshotRemoveFailed"},
		{executionErr, cleanupErr, "backendReportInvalid"},
	} {
		report, err := finishAdapter(ctx, BridgeReport{SchemaVersion: "partial"}, test.existing, test.cleanup)
		if err == nil || err.Error() != test.want || report.SchemaVersion != "" {
			t.Errorf("report=%+v err=%v want=%s", report, err, test.want)
		}
	}
}

// Helper-level evidence for error precedence and sanitization, not a timing
// claim about cancellation arriving at an arbitrary public return instant.
func TestAdapterLifecycleCombinedCleanupError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	primary := &AdapterError{Code: "backendReportInvalid", ExitCode: 6}
	canary := "PRIVATE_CLEANUP_PATH_SECRET"
	cleanup := fmt.Errorf("remove /private/%s: permission denied", canary)
	report, err := finishAdapter(ctx, BridgeReport{SchemaVersion: "partial"}, primary, cleanup)
	if err == nil || err.Error() != primary.Error() || !errors.Is(err, primary) {
		t.Fatalf("primary identity/text changed: %v", err)
	}
	var classified *AdapterError
	if !errors.As(err, &classified) || classified != primary || classified.ExitCode != 6 {
		t.Fatalf("primary classification changed: %+v", classified)
	}
	if report.SchemaVersion != "" || errors.Is(err, context.Canceled) || errors.Is(err, cleanup) {
		t.Fatalf("unexpected report/cancellation/raw cleanup: report=%+v err=%v", report, err)
	}
	var foundCleanup bool
	walkAdapterErrors(err, func(current error) {
		if strings.Contains(fmt.Sprintf("%v %+v %#v", current, current, current), canary) {
			t.Error("raw cleanup canary exposed in error chain")
		}
		if detail, ok := current.(*AdapterError); ok && detail.Code == "snapshotRemoveFailed" && detail.ExitCode == 5 {
			foundCleanup = true
		}
	})
	if !foundCleanup {
		t.Fatal("sanitized snapshotRemoveFailed/5 missing from primary error chain")
	}
}

func TestAdapterLifecycleFinalBoundaryIndependentOutcomes(t *testing.T) {
	primary := &AdapterError{Code: "protectedSourceChanged", ExitCode: 1}
	for _, test := range []struct {
		name             string
		primary, cleanup error
		canceled         bool
		wantCode         string
		wantExit         int
	}{
		{name: "success"},
		{name: "primary only", primary: primary, wantCode: primary.Code, wantExit: 1},
		{name: "cleanup only", cleanup: errors.New("PRIVATE_CLEANUP_PATH_SECRET"), wantCode: "snapshotRemoveFailed", wantExit: 5},
		{name: "cancel only", canceled: true, wantCode: "backendProcessFailed", wantExit: 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.canceled {
				cancel()
			}
			report, err := finishAdapter(ctx, BridgeReport{SchemaVersion: "complete"}, test.primary, test.cleanup)
			if test.wantCode == "" {
				if err != nil || report.SchemaVersion != "complete" {
					t.Fatalf("successful result lost: %+v %v", report, err)
				}
				return
			}
			var classified *AdapterError
			if !errors.As(err, &classified) || classified.Code != test.wantCode || classified.ExitCode != test.wantExit || err.Error() != test.wantCode || report.SchemaVersion != "" {
				t.Fatalf("report=%+v error=%v classification=%+v", report, err, classified)
			}
			if test.primary != nil && err != test.primary {
				t.Fatal("primary-only identity changed")
			}
			walkAdapterErrors(err, func(current error) {
				if strings.Contains(fmt.Sprintf("%v %+v %#v", current, current, current), "PRIVATE_CLEANUP_PATH_SECRET") {
					t.Error("raw cleanup exposed")
				}
			})
		})
	}
}

func walkAdapterErrors(err error, visit func(error)) {
	if err == nil {
		return
	}
	visit(err)
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range wrapped.Unwrap() {
			walkAdapterErrors(child, visit)
		}
	case interface{ Unwrap() error }:
		walkAdapterErrors(wrapped.Unwrap(), visit)
	}
}

func TestAdapterLifecycleOriginalVerificationWinsCancellation(t *testing.T) {
	request := lifecycleRequest(t, "exit 0")
	original := filepath.Join(request.ProjectRoot, "value.go")
	request.BackendExecutable = mutationTestBackend(t, fmt.Sprintf("printf changed > %q\nsleep 2", original))
	request.Timeout = 100 * time.Millisecond
	_, err := NewAdapter().Run(context.Background(), request)
	if err == nil || err.Error() != "protectedSourceChanged" {
		t.Fatalf("error=%v", err)
	}
}

func TestAdapterLifecycleEscalatesIgnoredTermination(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "pid")
	request := lifecycleRequest(t, fmt.Sprintf("trap '' TERM\n/usr/bin/sleep 8 &\necho $! > %q\nwait", pidPath))
	request.Timeout = 100 * time.Millisecond
	start := time.Now()
	_, err := NewAdapter().Run(context.Background(), request)
	if err == nil || err.Error() != "backendTimedOut" {
		t.Errorf("error=%v", err)
	}
	payload, readErr := os.ReadFile(pidPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(payload)))
	if parseErr != nil || pid < 1 {
		t.Fatalf("invalid fixture pid %q: %v", payload, parseErr)
	}
	if !lifecycleStopped(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Error("ignored-TERM descendant alive before fallback cleanup")
	}
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Errorf("escalation took %v", elapsed)
	}
}
