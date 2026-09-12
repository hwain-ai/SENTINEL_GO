package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReferenceCanceledUncoveredCandidateReturnsNoObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome, execution, err := executeReferenceCandidate(ctx, bridgeOptions{}, newReferenceCapture(), nil, plannedCandidate{})
	if !errors.Is(err, context.Canceled) || outcome.Status != "" || execution.Compile.State != "" {
		t.Fatalf("outcome=%+v execution=%+v err=%v", outcome, execution, err)
	}
}

func TestReferenceCompileStartFailurePreservesLegacyMapping(t *testing.T) {
	options := bridgeOptions{projectRoot: t.TempDir(), goBinary: "/missing/sentinel-go", timeout: time.Second}
	if status := runGo(context.Background(), options, "test"); status != "failed" {
		t.Fatalf("legacy start failure=%s want failed", status)
	}
	if _, err := runGoObservation(context.Background(), options, time.Second, "test"); err == nil {
		t.Fatal("reference accepted missing process")
	}
}

func referenceRecorder(t *testing.T, calls string) string {
	t.Helper()
	return bridgeScript(t, fmt.Sprintf(`printf '%%s\n' "$*" >> %q
count=$(wc -l < %q)
nonce=$(printf '%%032x' "$count")
printf '{"schemaVersion":"sentinel-go-reference-execution-v1","profile":"replay-identity-v1","requestId":"%%s","timeoutMilliseconds":%%s,"runnerSchema":"sentinel-go-typed-runner-v1","nonce":"%%s","status":"passed","inventoryCount":1,"inputSha256":"%s","replay":{"inventorySha256":"%s","resultsSha256":"%s","failureSha256":""},"referenceOnly":true,"certified":false}\n' "$6" "$8" "$nonce"`, calls, calls, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)))
}

func TestReferenceCaptureUsesFreshCorrelatedRequestsAndSelectedTimeout(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	options := bridgeOptions{projectRoot: t.TempDir(), goBinary: bridgeScript(t, "exit 0"), referenceRunnerBinary: referenceRecorder(t, calls)}
	capture := newReferenceCapture()
	first, err := capture.invoke(context.Background(), options, "control", "", 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := capture.invoke(context.Background(), options, "candidate", "candidate", 1, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestID == second.RequestID || len(capture.requestIDs) != 2 || len(capture.nonces) != 2 {
		t.Fatalf("first=%+v second=%+v capture=%+v", first, second, capture)
	}
	lines := strings.Split(strings.TrimSpace(string(mustRead(t, calls))), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "--timeout-ms 1000") || !strings.HasSuffix(lines[1], "--timeout-ms 200") {
		t.Fatalf("calls=%q", lines)
	}
	for i, receipt := range []referenceReceipt{first, second} {
		want := []string{"--project-root", options.projectRoot, "--go-binary", options.goBinary, "--request-id", receipt.RequestID, "--timeout-ms", fmt.Sprint(receipt.TimeoutMilliseconds)}
		if !slices.Equal(strings.Fields(lines[i]), want) {
			t.Errorf("argv=%q want=%q", strings.Fields(lines[i]), want)
		}
	}
	if len(capture.invocations) != 2 || capture.invocations[0] != (capturedInvocation{phase: "control", ordinal: 1, timeout: time.Second}) || capture.invocations[1] != (capturedInvocation{phase: "candidate", candidateID: "candidate", ordinal: 1, timeout: 200 * time.Millisecond}) {
		t.Fatalf("capture=%+v", capture.invocations)
	}
}

func TestReferenceCaptureDomainsAndRunsAreIndependent(t *testing.T) {
	for i := 0; i < 2; i++ {
		capture := newReferenceCapture()
		capture.random = bytes.NewReader(make([]byte, 16))
		id, err := capture.nextRequestID()
		if err != nil {
			t.Fatal(err)
		}
		if err := capture.acceptNonce(id); err != nil {
			t.Fatal("request and runner nonce domains conflated")
		}
	}
	capture := newReferenceCapture()
	capture.random = bytes.NewReader(nil)
	if _, err := capture.nextRequestID(); err == nil || len(capture.requestIDs) != 0 {
		t.Fatal("random failure issued request")
	}
}

func TestReferenceIdentityDisagreementMatrixPreservesReceipts(t *testing.T) {
	base, err := decodeReferenceReceipt([]byte(validReferenceReceipt))
	if err != nil {
		t.Fatal(err)
	}
	controls := []referenceReceipt{base, base}
	changes := map[string]func(*referenceReceipt){
		"status":    func(r *referenceReceipt) { r.Status = "toolError" },
		"input":     func(r *referenceReceipt) { r.InputSHA256 = strings.Repeat("d", 64) },
		"count":     func(r *referenceReceipt) { r.InventoryCount++ },
		"inventory": func(r *referenceReceipt) { r.Replay.InventorySHA256 = strings.Repeat("d", 64) },
		"results":   func(r *referenceReceipt) { r.Replay.ResultsSHA256 = strings.Repeat("d", 64) },
		"failure":   func(r *referenceReceipt) { r.Replay.FailureSHA256 = strings.Repeat("d", 64) },
		"null":      func(r *referenceReceipt) { r.Replay = nil; r.Status = "toolError" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			modified := base
			replay := *base.Replay
			modified.Replay = &replay
			change(&modified)
			pair := []referenceReceipt{base, modified}
			before, _ := json.Marshal(pair)
			if got := referenceReplayOutcome(controls, pair); got != "toolError" {
				t.Fatalf("outcome=%s", got)
			}
			after, _ := json.Marshal(pair)
			if !bytes.Equal(before, after) {
				t.Fatal("raw receipts modified")
			}
			if name != "failure" && controlEvidenceValid(pair) {
				t.Fatal("inconsistent controls accepted")
			}
		})
	}
	for _, status := range []string{"passed", "assertionFailure", "compileError", "runtimeError", "timedOut", "toolError"} {
		r := base
		replay := *base.Replay
		r.Replay = &replay
		r.Status = status
		r.InputSHA256 = strings.Repeat("d", 64)
		r.Replay.ResultsSHA256 = strings.Repeat("e", 64)
		want := status
		if status == "passed" {
			want = "survived"
		}
		if status == "assertionFailure" {
			want = "killed"
			r.Replay.FailureSHA256 = strings.Repeat("f", 64)
		}
		if got := referenceReplayOutcome(controls, []referenceReceipt{r, r}); got != want {
			t.Errorf("%s=%s want=%s", status, got, want)
		}
	}
}

func TestReferenceTransportFailuresReturnNoReceipt(t *testing.T) {
	for name, body := range map[string]string{
		"empty": "exit 0", "malformed": "printf '{}'", "nonzero": "printf 'private-output'; echo private-path >&2; exit 9",
		"oversize":      "head -c 16385 /dev/zero | tr '\\000' ' '",
		"wrong request": "printf '%s' '" + validReferenceReceipt + "'",
		"wrong timeout": "printf '" + strings.Replace(validReferenceReceipt, "0123456789abcdef0123456789abcdef", "%s", 1) + "' \"$6\"",
	} {
		t.Run(name, func(t *testing.T) {
			options := bridgeOptions{projectRoot: t.TempDir(), goBinary: "unused", referenceRunnerBinary: bridgeScript(t, body)}
			receipt, err := newReferenceCapture().invoke(context.Background(), options, "control", "", 1, 900*time.Millisecond)
			if err == nil || receipt != (referenceReceipt{}) {
				t.Fatalf("receipt=%+v err=%v", receipt, err)
			}
			if strings.Contains(err.Error(), options.projectRoot) || strings.Contains(err.Error(), "private") {
				t.Fatal("raw diagnostic leaked")
			}
		})
	}
	options := bridgeOptions{projectRoot: t.TempDir(), referenceRunnerBinary: "/missing/sentinel-reference"}
	if _, err := newReferenceCapture().invoke(context.Background(), options, "control", "", 1, time.Second); err == nil {
		t.Fatal("start failure accepted")
	}
}

func TestReferenceCompileTimeoutSkipsReplaysAndRestores(t *testing.T) {
	source := "package sample\nfunc Positive(x int) bool { return x > 0 }\n"
	project := bridgeFixture(t, source, "package sample\n")
	_, plan, err := discoverSource(project, "example.test/bridge", "value.go", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range plan {
		plan[i].covered = true
	}
	marker, count, calls := filepath.Join(t.TempDir(), "pid"), filepath.Join(t.TempDir(), "count"), filepath.Join(t.TempDir(), "calls")
	t.Cleanup(func() { cleanupBridgeFixturePID(t, marker) })
	script := bridgeScript(t, fmt.Sprintf("echo call >> %q\nif [ \"$(wc -l < %q)\" -eq 1 ]; then echo $$ > %q; sleep 2; fi", count, count, marker))
	options := bridgeOptions{projectRoot: project, goBinary: script, referenceRunnerBinary: referenceRecorder(t, calls), timeout: time.Second, mutantTimeout: 100 * time.Millisecond}
	control, _ := decodeReferenceReceipt([]byte(validReferenceReceipt))
	outcomes, stages, err := executeReferencePlan(context.Background(), options, newReferenceCapture(), []referenceReceipt{control, control}, plan)
	if err != nil {
		t.Fatal(err)
	}
	first := stages[0]
	if outcomes[0].Status != "timedOut" || first.Compile.Observation.Status != "timedOut" || !first.Compile.Observation.DeadlineExceeded || first.Compile.Observation.ExitCode != -1 {
		t.Fatalf("outcome=%+v stage=%+v", outcomes[0], first)
	}
	for _, replay := range first.Replays {
		if replay.State != "notExecuted" || replay.Reason != "compileBlocked" || replay.Receipt != nil {
			t.Fatalf("replay=%+v", replay)
		}
	}
	if outcomes[1].Status != "survived" || strings.Count(string(mustRead(t, calls)), "\n") != 2 {
		t.Fatal("later candidate did not execute exactly twice")
	}
	if string(mustRead(t, filepath.Join(project, "value.go"))) != source {
		t.Fatal("source not restored")
	}
	if !bridgeFixtureStopped(bridgeFixturePID(t, marker)) {
		t.Fatal("compile child alive before fallback cleanup")
	}
}

func TestReferenceCoverageHashesTheParsedBytes(t *testing.T) {
	project := t.TempDir()
	payload := "mode: set\nexample.test/bridge/value.go:2.1,2.60 1 1\n"
	options := bridgeOptions{projectRoot: project, goBinary: "unused", timeout: time.Second, runnerBinary: bridgeScript(t, `for last; do :; done
printf '%s' '`+payload+`' > "$last"
printf '%s' '`+strings.Replace(lifecycleTimedOut, "timedOut", "passed", 1)+`'`)}
	profile, observed, err := freshReferenceCoverage(context.Background(), options)
	if err != nil || len(profile["example.test/bridge/value.go"]) != 1 || observed.ProfileSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(payload))) {
		t.Fatalf("profile=%+v observed=%+v err=%v", profile, observed, err)
	}
	if _, err := readBoundedFile(filepath.Join(project, "target", "sentinel-coverage", "coverage.out"), len(payload)-1); err == nil {
		t.Fatal("coverage read limit ignored")
	}
}

func TestReferenceCompileCleanupFailureIsNotTypedObservation(t *testing.T) {
	for _, result := range []commandResult{{started: false}, {started: true, exitCode: 0, cleanupErr: errors.New("private failure")}, {started: true, exitCode: 1, runErr: errors.New("exit"), cleanupErr: errors.New("cleanup")}} {
		if observation, err := referenceCompileObservation(context.Background(), result); err == nil || observation != (compileObservation{}) {
			t.Fatalf("observation=%+v err=%v", observation, err)
		}
	}
}

func TestReferenceCompileSupervisorFailureIsNotCompileError(t *testing.T) {
	observation, err := referenceCompileObservation(context.Background(), commandResult{started: true, exitCode: 0, runErr: exec.ErrWaitDelay})
	if err == nil || observation != (compileObservation{}) {
		t.Fatalf("supervisor failure became observation=%+v err=%v", observation, err)
	}
}

func TestReferenceReceiptCleanupFailureDiscardsActualReceipt(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	options := bridgeOptions{projectRoot: t.TempDir(), referenceRunnerBinary: referenceRecorder(t, calls), goBinary: "unused"}
	capture := newReferenceCapture()
	capture.supervise = func(command *exec.Cmd, signal syscall.Signal, grace time.Duration) (error, error) {
		runErr, cleanupErr := runCommandGroup(command, signal, grace)
		return runErr, errors.Join(cleanupErr, errors.New("injected cleanup failure"))
	}
	receipt, err := capture.invoke(context.Background(), options, "control", "", 1, time.Second)
	if err == nil || receipt != (referenceReceipt{}) || len(capture.nonces) != 0 {
		t.Fatalf("receipt=%+v err=%v nonces=%v", receipt, err, capture.nonces)
	}
	if strings.Count(string(mustRead(t, calls)), "\n") != 1 {
		t.Fatal("runner was not actually invoked")
	}
}

func TestReferenceUncoveredCandidatesHaveNoCompileOrReplays(t *testing.T) {
	source := "package sample\nfunc Positive(x int) bool { return x > 0 }\n"
	project := bridgeFixture(t, source, "package sample\n")
	marker, calls := filepath.Join(t.TempDir(), "compiled"), filepath.Join(t.TempDir(), "calls")
	runner := bridgeScript(t, `for last; do :; done
printf 'mode: set\n' > "$last"
printf '%s' '`+strings.Replace(lifecycleTimedOut, "timedOut", "passed", 1)+`'`)
	options := bridgeOptions{projectRoot: project, goBinary: bridgeScript(t, fmt.Sprintf("touch %q", marker)), runnerBinary: runner, referenceRunnerBinary: referenceRecorder(t, calls), referenceRunID: strings.Repeat("0", 32), timeout: time.Second, sources: []string{"value.go"}}
	r, code, err := executeReference(context.Background(), options)
	if err != nil || code != 0 || len(r.Outcomes) != 2 {
		t.Fatalf("code=%d report=%+v err=%v", code, r, err)
	}
	for i, outcome := range r.Outcomes {
		if outcome.Status != "uncovered" || outcome.DurationNanos != 0 || r.CandidateExecutions[i].Compile.State != "notExecuted" {
			t.Fatalf("outcome=%+v stage=%+v", outcome, r.CandidateExecutions[i])
		}
		for _, replay := range r.CandidateExecutions[i].Replays {
			if replay.State != "notExecuted" || replay.Reason != "uncovered" || replay.Receipt != nil {
				t.Fatalf("replay=%+v", replay)
			}
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("uncovered candidate compiled")
	}
	if strings.Count(string(mustRead(t, calls)), "\n") != 2 {
		t.Fatal("uncovered candidate replayed")
	}
}

func TestReferenceRunFailuresEmitNoEnvelopeAndRestore(t *testing.T) {
	for _, tc := range []struct {
		name       string
		code       int
		diagnostic string
	}{
		{"coverage", 4, "baselineFailed\n"}, {"control mismatch", 4, "baselineFailed\n"}, {"control failure", 4, "baselineFailed\n"},
		{"control contract", 6, "backendError\n"}, {"candidate contract", 6, "backendError\n"}, {"compile start", 6, "backendError\n"}, {"dependency", 5, "dependencyError\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "package sample\nfunc Positive(x int) bool { return x > 0 }\n"
			project := bridgeFixture(t, source, "package sample\n")
			calls := filepath.Join(t.TempDir(), "calls")
			recorder := referenceRecorder(t, calls)
			legacy := referenceCoverageRecorder(t)
			goBinary := bridgeScript(t, "exit 0")
			body := string(mustRead(t, recorder))
			switch tc.name {
			case "coverage":
				legacy = bridgeScript(t, "printf '{}'")
			case "control mismatch":
				body = strings.Replace(body, "nonce=$(", `if [ "$count" -eq 2 ]; then input=`+strings.Repeat("d", 64)+`; else input=`+strings.Repeat("a", 64)+`; fi`+"\nnonce=$(", 1)
				body = strings.Replace(body, strings.Repeat("a", 64)+`","replay"`, `%s","replay"`, 1)
				body = strings.Replace(body, `"$6" "$8" "$nonce"`, `"$6" "$8" "$nonce" "$input"`, 1)
			case "control failure":
				body = strings.Replace(body, `"status":"passed"`, `"status":"toolError"`, 1)
			case "control contract":
				body = "#!/usr/bin/bash\nprintf '{}'\n"
			case "candidate contract":
				body = strings.Replace(body, "nonce=$(", `if [ "$count" -eq 3 ]; then printf '{}'; exit 0; fi`+"\nnonce=$(", 1)
			case "compile start":
				body = strings.Replace(body, "nonce=$(", fmt.Sprintf("if [ \"$count\" -eq 2 ]; then rm -- %q; fi\nnonce=$(", goBinary), 1)
			case "dependency":
				if err := os.Remove(filepath.Join(project, "go.mod")); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(recorder, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := run([]string{"--project-root", project, "--go-binary", goBinary, "--runner-binary", legacy, "--reference-runner-binary", recorder, "--reference-run-id", strings.Repeat("0", 32), "--timeout-ms", "1000", "--source", "value.go"}, &stdout, &stderr)
			if code != tc.code || stdout.Len() != 0 || stderr.String() != tc.diagnostic {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if string(mustRead(t, filepath.Join(project, "value.go"))) != source {
				t.Fatal("source not restored")
			}
		})
	}
}

func referenceCoverageRecorder(t *testing.T) string {
	t.Helper()
	return bridgeScript(t, `for last; do :; done
printf 'mode: set\nexample.test/bridge/value.go:2.1,2.60 1 1\n' > "$last"
printf '%s' '`+strings.Replace(lifecycleTimedOut, "timedOut", "passed", 1)+`'`)
}

func TestReferenceWatchdogStopsChildBeforeReturning(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pid")
	t.Cleanup(func() { cleanupBridgeFixturePID(t, marker) })
	options := bridgeOptions{projectRoot: t.TempDir(), referenceRunnerBinary: bridgeScript(t, fmt.Sprintf("echo $$ > %q\nsleep 8", marker))}
	start := time.Now()
	receipt, err := newReferenceCapture().invoke(context.Background(), options, "control", "", 1, 50*time.Millisecond)
	if err == nil || receipt != (referenceReceipt{}) || time.Since(start) > 6*time.Second {
		t.Fatalf("receipt=%+v err=%v duration=%s", receipt, err, time.Since(start))
	}
	if !bridgeFixtureStopped(bridgeFixturePID(t, marker)) {
		t.Fatal("runner alive before fallback cleanup")
	}
}

func TestReferenceSignalAbortsRestoresAndStopsFurtherStages(t *testing.T) {
	for _, phase := range []string{"compile", "replay", "restoreFailure"} {
		t.Run(phase, func(t *testing.T) {
			source := "package sample\nfunc Positive(x int) bool { return x > 0 }\n"
			project := bridgeFixture(t, source, "package sample\n")
			marker, calls := filepath.Join(t.TempDir(), "pid"), filepath.Join(t.TempDir(), "calls")
			recorder := referenceRecorder(t, calls)
			goBinary := bridgeScript(t, "exit 0")
			if phase == "replay" {
				body := string(mustRead(t, recorder))
				body = strings.Replace(body, "count=$(wc -l", fmt.Sprintf("if [ -f %q ] && [ \"$(wc -l < %q)\" -ge 3 ]; then echo $$ > %q; sleep 8; fi\ncount=$(wc -l", calls, calls, marker), 1)
				if err := os.WriteFile(recorder, []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				body := fmt.Sprintf("echo $$ > %q\nsleep 8", marker)
				if phase == "restoreFailure" {
					body = "mv value.go saved.go\nmkdir value.go\n" + body
				}
				goBinary = bridgeScript(t, body)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestLifecycleBridgeSignalHelper$", "--", "--project-root", project, "--go-binary", goBinary, "--runner-binary", referenceCoverageRecorder(t), "--reference-runner-binary", recorder, "--reference-run-id", strings.Repeat("0", 32), "--timeout-ms", "10000", "--source", "value.go")
			command.Env = append(os.Environ(), "SENTINEL_BRIDGE_SIGNAL_HELPER=1")
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			done := startBridgeSignalFixture(t, command, marker)
			waitBridgeFile(t, marker)
			if err := command.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				t.Fatal("shutdown unbounded")
			}
			if command.ProcessState.ExitCode() != 6 || stdout.Len() != 0 || stderr.String() != "backendError\n" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", command.ProcessState.ExitCode(), stdout.String(), stderr.String())
			}
			if phase != "restoreFailure" && string(mustRead(t, filepath.Join(project, "value.go"))) != source {
				t.Fatal("source not restored")
			}
			if phase == "restoreFailure" {
				if info, err := os.Stat(filepath.Join(project, "value.go")); err != nil || !info.IsDir() {
					t.Fatal("restoration failure fixture did not run")
				}
			}
			want := 2
			if phase == "replay" {
				want = 3
			}
			if count := strings.Count(string(mustRead(t, calls)), "\n"); count != want {
				t.Fatalf("calls=%d want=%d", count, want)
			}
			if !bridgeFixtureStopped(bridgeFixturePID(t, marker)) {
				t.Fatal("child alive before fallback cleanup")
			}
		})
	}
}

func TestReferenceSignalReclaimsActualE1TestGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "test-pid")
	source := "package sample\nfunc Positive(x int) bool { return x > 0 }\n"
	tests := fmt.Sprintf("package sample\nimport (\"testing\";\"os\";\"strconv\";\"time\")\nfunc TestPrivateCancel(t *testing.T) { if Positive(0) { os.WriteFile(%q,[]byte(strconv.Itoa(os.Getpid())),0600); time.Sleep(8*time.Second); t.Fail() }; if !Positive(1) { t.Fail() } }\n", marker)
	project := bridgeFixture(t, source, tests)
	command := exec.Command(os.Args[0], "-test.run=^TestLifecycleBridgeSignalHelper$", "--", "--project-root", project, "--go-binary", filepath.Join(runtime.GOROOT(), "bin", "go"), "--runner-binary", buildRunner(t), "--reference-runner-binary", buildReferenceRunner(t), "--reference-run-id", strings.Repeat("0", 32), "--timeout-ms", "10000", "--source", "value.go")
	command.Env = append(os.Environ(), "SENTINEL_BRIDGE_SIGNAL_HELPER=1")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	done := startBridgeSignalFixture(t, command, marker)
	waitBridgeFile(t, marker)
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("actual E1 cancellation unbounded")
	}
	if command.ProcessState.ExitCode() != 6 || stdout.Len() != 0 || stderr.String() != "backendError\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", command.ProcessState.ExitCode(), stdout.String(), stderr.String())
	}
	if !bridgeFixtureStopped(bridgeFixturePID(t, marker)) {
		t.Fatal("actual E1 test alive before fallback cleanup")
	}
	if string(mustRead(t, filepath.Join(project, "value.go"))) != source || string(mustRead(t, filepath.Join(project, "value_test.go"))) != tests {
		t.Fatal("actual E1 source not restored")
	}
}

func TestReferenceCaptureRejectsDuplicateNonceAndRequestID(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	recorder := referenceRecorder(t, calls)
	options := bridgeOptions{projectRoot: t.TempDir(), goBinary: bridgeScript(t, "exit 0"), referenceRunnerBinary: recorder}
	capture := newReferenceCapture()
	if _, err := capture.invoke(context.Background(), options, "control", "", 1, time.Second); err != nil {
		t.Fatal(err)
	}
	duplicateNonce := bridgeScript(t, fmt.Sprintf(`printf '{"schemaVersion":"sentinel-go-reference-execution-v1","profile":"replay-identity-v1","requestId":"%%s","timeoutMilliseconds":1000,"runnerSchema":"sentinel-go-typed-runner-v1","nonce":"00000000000000000000000000000001","status":"toolError","inventoryCount":1,"inputSha256":"%s","replay":null,"referenceOnly":true,"certified":false}\n' "$6"`, strings.Repeat("a", 64)))
	options.referenceRunnerBinary = duplicateNonce
	if _, err := capture.invoke(context.Background(), options, "control", "", 2, time.Second); err == nil {
		t.Fatal("duplicate nonce accepted")
	}

	duplicateRandom := bytes.NewReader(make([]byte, 32))
	capture = &referenceCapture{random: duplicateRandom, requestIDs: make(map[string]struct{}), nonces: make(map[string]struct{})}
	if _, err := capture.nextRequestID(); err != nil {
		t.Fatal(err)
	}
	if _, err := capture.nextRequestID(); err == nil {
		t.Fatal("duplicate request ID accepted")
	}
}

func TestReferenceIdentityPolicies(t *testing.T) {
	control := referenceReceipt{Status: "passed", InventoryCount: 1, InputSHA256: strings.Repeat("a", 64), Replay: &referenceReplay{InventorySHA256: strings.Repeat("b", 64), ResultsSHA256: strings.Repeat("c", 64)}}
	controls := []referenceReceipt{control, control}
	if !controlEvidenceValid(controls) {
		t.Fatal("matching controls rejected")
	}
	assertion := referenceReceipt{Status: "assertionFailure", InventoryCount: 1, InputSHA256: strings.Repeat("d", 64), Replay: &referenceReplay{InventorySHA256: strings.Repeat("b", 64), ResultsSHA256: strings.Repeat("e", 64), FailureSHA256: strings.Repeat("f", 64)}}
	if got := referenceReplayOutcome(controls, []referenceReceipt{assertion, assertion}); got != "killed" {
		t.Fatalf("outcome=%s", got)
	}
	null := assertion
	null.Replay = nil
	if got := referenceReplayOutcome(controls, []referenceReceipt{null, null}); got != "toolError" {
		t.Fatalf("null outcome=%s", got)
	}
	mismatch := assertion
	mismatch.InputSHA256 = strings.Repeat("e", 64)
	if got := referenceReplayOutcome(controls, []referenceReceipt{assertion, mismatch}); got != "toolError" {
		t.Fatalf("mismatch outcome=%s", got)
	}
}

func TestReferenceProfileRecordsActualStagesAndRestoresSource(t *testing.T) {
	source := "package sample\nfunc Positive(x int) bool { return x > 0 }\n"
	project := bridgeFixture(t, source, "package sample\n")
	calls := filepath.Join(t.TempDir(), "calls")
	referenceRunner := referenceRecorder(t, calls)
	typedRunner := bridgeScript(t, `for last; do :; done
printf 'mode: set\nexample.test/bridge/value.go:2.1,2.60 1 1\n' > "$last"
printf '{"schemaVersion":"sentinel-go-typed-runner-v1","nonce":"0123456789abcdef0123456789abcdef","status":"passed","inventoryCount":1}\n'`)
	goBinary := bridgeScript(t, "exit 0")
	var stdout, stderr bytes.Buffer
	exit := run([]string{
		"--project-root", project, "--go-binary", goBinary, "--runner-binary", typedRunner,
		"--reference-runner-binary", referenceRunner, "--reference-run-id", "0123456789abcdef0123456789abcdef",
		"--timeout-ms", "1000", "--mutant-timeout-ms", "200", "--source", "value.go",
	}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"backendCommit", "backendName", "candidateExecutions", "candidates", "certified", "controls", "coverage", "outcomes", "profile", "referenceOnly", "runId", "schemaVersion", "sourceInventory"}
	gotKeys := make([]string, 0, len(raw))
	for key := range raw {
		gotKeys = append(gotKeys, key)
	}
	slices.Sort(gotKeys)
	if !slices.Equal(gotKeys, wantKeys) {
		t.Fatalf("keys=%q", gotKeys)
	}
	var report referenceReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Controls) != 2 || len(report.Candidates) != 2 || len(report.Outcomes) != 2 || len(report.CandidateExecutions) != 2 {
		t.Fatalf("report=%+v", report)
	}
	for index := range report.Outcomes {
		if report.Outcomes[index].Status != "survived" || report.CandidateExecutions[index].Compile.Observation == nil || len(report.CandidateExecutions[index].Replays) != 2 {
			t.Errorf("candidate %d outcome=%+v execution=%+v", index, report.Outcomes[index], report.CandidateExecutions[index])
		}
	}
	lines := strings.Split(strings.TrimSpace(string(mustRead(t, calls))), "\n")
	if len(lines) != 6 {
		t.Fatalf("reference calls=%q", lines)
	}
	for index, line := range lines {
		want := "--timeout-ms 1000"
		if index >= 2 {
			want = "--timeout-ms 200"
		}
		if !strings.HasSuffix(line, want) {
			t.Errorf("call %d=%q", index, line)
		}
	}
	if got := string(mustRead(t, filepath.Join(project, "value.go"))); got != source {
		t.Fatalf("source not restored: %q", got)
	}
}

func TestReferenceCompileFailureSkipsReplaysAndContinues(t *testing.T) {
	project := bridgeFixture(t, "package sample\nfunc Positive(x int) bool { return x > 0 }\n", "package sample\n")
	_, plan, err := discoverSource(project, "example.test/bridge", "value.go", nil)
	if err != nil || len(plan) != 2 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	for index := range plan {
		plan[index].covered = true
	}
	count := filepath.Join(t.TempDir(), "compile-count")
	goBinary := bridgeScript(t, fmt.Sprintf(`echo call >> %q
if [ "$(wc -l < %q)" -eq 1 ]; then exit 1; fi`, count, count))
	referenceCalls := filepath.Join(t.TempDir(), "reference-calls")
	options := bridgeOptions{projectRoot: project, goBinary: goBinary, referenceRunnerBinary: referenceRecorder(t, referenceCalls), timeout: time.Second}
	control := referenceReceipt{Status: "passed", InventoryCount: 1, Replay: &referenceReplay{InventorySHA256: strings.Repeat("b", 64)}}
	outcomes, executions, err := executeReferencePlan(context.Background(), options, newReferenceCapture(), []referenceReceipt{control, control}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if outcomes[0].Status != "compileError" || len(executions[0].Replays) != 2 || executions[0].Replays[0].State != "notExecuted" || outcomes[1].Status != "survived" {
		t.Fatalf("outcomes=%+v executions=%+v", outcomes, executions)
	}
	if lines := strings.Fields(string(mustRead(t, referenceCalls))); len(lines) == 0 {
		t.Fatal("later candidate did not replay")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func buildReferenceRunner(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."))
	output := filepath.Join(t.TempDir(), "sentinel-go-reference-runner")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-o", output, "./cmd/sentinel-go-reference-runner")
	command.Dir = repositoryRoot
	command.Env = os.Environ()
	if payload, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build reference runner: %v: %s", err, payload)
	}
	return output
}

func TestReferenceActualE1ReceiptsAndMutationReplays(t *testing.T) {
	referenceBinary, legacyBinary := buildReferenceRunner(t), buildRunner(t)
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	for _, tc := range []struct {
		name, body, want string
		null             bool
	}{
		{"assertion", `if Positive(0) { t.Fatal("private failure") }; if !Positive(1) { t.Fail() }`, "killed", false},
		{"unsupported", `if Positive(0) { fail := t.Fail; fail() }; if !Positive(1) { fail := t.Fail; fail() }`, "toolError", true},
		{"survived", `_ = Positive(1)`, "survived", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "package sample\nfunc Positive(x int) bool { return x > 0 }\n"
			tests := "package sample\nimport \"testing\"\nfunc TestPrivate(t *testing.T) { " + tc.body + " }\n"
			project := bridgeFixture(t, source, tests)
			options := bridgeOptions{projectRoot: project, goBinary: goBinary, runnerBinary: legacyBinary, referenceRunnerBinary: referenceBinary, referenceRunID: strings.Repeat("0", 32), timeout: 20 * time.Second, sources: []string{"value.go"}}
			report, code, err := executeReference(context.Background(), options)
			if err != nil || code != 0 {
				t.Fatalf("code=%d err=%v", code, err)
			}
			if len(report.Controls) != 2 || len(report.Outcomes) != 2 {
				t.Fatalf("report=%+v", report)
			}
			for i, outcome := range report.Outcomes {
				if outcome.Status != tc.want {
					t.Errorf("candidate %d status=%s want=%s", i, outcome.Status, tc.want)
				}
				stages := report.CandidateExecutions[i]
				if stages.Compile.Observation == nil || stages.Compile.Observation.Status != "passed" {
					t.Fatalf("compile=%+v", stages.Compile)
				}
				for _, stage := range stages.Replays {
					if stage.State != "executed" || stage.Receipt == nil || (stage.Receipt.Replay == nil) != tc.null {
						t.Errorf("replay=%+v", stage)
					}
					if stage.Receipt != nil && stage.Receipt.InputSHA256 == report.Controls[0].InputSHA256 {
						t.Error("mutant borrowed original input identity")
					}
				}
			}
			var out bytes.Buffer
			if err := encodeReferenceReport(context.Background(), &out, report); err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{project, "TestPrivate", "private failure", "value_test.go"} {
				if strings.Contains(out.String(), private) {
					t.Errorf("output leaked %q", private)
				}
			}
			if string(mustRead(t, filepath.Join(project, "value.go"))) != source || string(mustRead(t, filepath.Join(project, "value_test.go"))) != tests {
				t.Fatal("actual runner left source modified")
			}
		})
	}
}

func TestReferenceActualE1HangingFixtureTimesOutAndCleans(t *testing.T) {
	binary := buildReferenceRunner(t)
	marker := filepath.Join(t.TempDir(), "test-pid")
	tests := fmt.Sprintf("package sample\nimport (\"testing\";\"os\";\"strconv\";\"time\")\nfunc TestPrivateHang(t *testing.T) { os.WriteFile(%q,[]byte(strconv.Itoa(os.Getpid())),0600); time.Sleep(8*time.Second) }\n", marker)
	project := bridgeFixture(t, "package sample\n", tests)
	t.Cleanup(func() { cleanupBridgeFixturePID(t, marker) })
	options := bridgeOptions{projectRoot: project, goBinary: filepath.Join(runtime.GOROOT(), "bin", "go"), referenceRunnerBinary: binary}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	receipt, err := newReferenceCapture().invoke(ctx, options, "candidate", strings.Repeat("a", 64), 1, 1500*time.Millisecond)
	if err != nil || receipt.Status != "timedOut" || receipt.Replay == nil {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	pid := bridgeFixturePID(t, marker)
	if !bridgeFixtureStopped(pid) {
		t.Fatal("real test process alive before fallback cleanup")
	}
	if string(mustRead(t, filepath.Join(project, "value_test.go"))) != tests {
		t.Fatal("test source not restored")
	}
}
