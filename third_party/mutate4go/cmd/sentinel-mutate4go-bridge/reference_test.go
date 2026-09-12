package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

type shortReferenceWriter struct{ calls int }

func (writer *shortReferenceWriter) Write(payload []byte) (int, error) {
	writer.calls++
	return len(payload) - 1, nil
}

func TestReferenceReportOutputBoundsShortWriteAndCancellation(t *testing.T) {
	r := validReferenceReportFixture(t)
	var base bytes.Buffer
	if err := encodeReferenceReport(context.Background(), &base, r); err != nil {
		t.Fatal(err)
	}
	r.Candidates[0].Operator = strings.Repeat("x", referenceReportLimit-base.Len()+len(r.Candidates[0].Operator))
	var out bytes.Buffer
	if err := encodeReferenceReport(context.Background(), &out, r); err != nil || out.Len() != referenceReportLimit {
		t.Fatalf("boundary len=%d err=%v", out.Len(), err)
	}
	out.Reset()
	r.Candidates[0].Operator += "x"
	if err := encodeReferenceReport(context.Background(), &out, r); err == nil || out.Len() != 0 {
		t.Fatalf("oversize len=%d err=%v", out.Len(), err)
	}
	r = validReferenceReportFixture(t)
	short := &shortReferenceWriter{}
	if err := encodeReferenceReport(context.Background(), short, r); err != io.ErrShortWrite || short.calls != 1 {
		t.Fatalf("short err=%v calls=%d", err, short.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := encodeReferenceReport(ctx, &out, r); err != context.Canceled || out.Len() != 0 {
		t.Fatalf("cancel len=%d err=%v", out.Len(), err)
	}
	r.Outcomes[0].Status = "killed"
	if err := encodeReferenceReport(context.Background(), &out, r); err == nil || out.Len() != 0 {
		t.Fatalf("contract len=%d err=%v", out.Len(), err)
	}
}

func validReferenceReportFixture(t *testing.T) referenceReport {
	t.Helper()
	control, err := decodeReferenceReceipt([]byte(validReferenceReceipt))
	if err != nil {
		t.Fatal(err)
	}
	second := control
	second.RequestID = strings.Repeat("1", 32)
	second.Nonce = strings.Repeat("2", 32)
	id := strings.Repeat("d", 64)
	return referenceReport{SchemaVersion: "sentinel-mutate4go-reference-report-v1", Profile: "replay-identity-v1", RunID: strings.Repeat("0", 32), BackendName: "mutate4go", BackendCommit: backendCommit, ReferenceOnly: true,
		Coverage: referenceCoverage{ProducerSchema: "sentinel-go-typed-runner-v1", Status: "passed", ProfileSHA256: strings.Repeat("a", 64)}, Controls: []referenceReceipt{control, second},
		SourceInventory: []sourceInventory{{Path: "value.go", SHA256: strings.Repeat("a", 64), CandidateCount: 1}}, Candidates: []machineCandidate{{ID: id, SourceFile: "value.go", Line: 2, Column: 2, Operator: "replace"}}, Outcomes: []machineOutcome{{CandidateID: id, Status: "uncovered"}},
		CandidateExecutions: []candidateExecution{{CandidateID: id, Compile: compileStage{State: "notExecuted", Reason: "uncovered"}, Replays: []replayStage{{Ordinal: 1, State: "notExecuted", Reason: "uncovered"}, {Ordinal: 2, State: "notExecuted", Reason: "uncovered"}}}}}
}

func TestReferenceReportRejectsImpossibleStagesAndCorrespondence(t *testing.T) {
	cases := map[string]func(*referenceReport){
		"unknown replay state":       func(r *referenceReport) { r.CandidateExecutions[0].Replays[0].State = "unknown" },
		"uncovered compileBlocked":   func(r *referenceReport) { r.CandidateExecutions[0].Replays[0].Reason = "compileBlocked" },
		"uncovered killed":           func(r *referenceReport) { r.Outcomes[0].Status = "killed" },
		"invalid source digest":      func(r *referenceReport) { r.SourceInventory[0].SHA256 = "" },
		"unknown source":             func(r *referenceReport) { r.Candidates[0].SourceFile = "other.go" },
		"negative duration":          func(r *referenceReport) { r.Outcomes[0].DurationNanos = -1 },
		"uncovered nonzero duration": func(r *referenceReport) { r.Outcomes[0].DurationNanos = 1 },
		"bad control":                func(r *referenceReport) { r.Controls[0].Certified = true },
		"duplicate nonce":            func(r *referenceReport) { r.Controls[1].Nonce = r.Controls[0].Nonce },
		"nil candidates": func(r *referenceReport) {
			r.Candidates = nil
			r.Outcomes = nil
			r.CandidateExecutions = nil
			r.SourceInventory[0].CandidateCount = 0
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := validReferenceReportFixture(t)
			change(&r)
			if err := validateReferenceReport(r); err == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
	for _, status := range []string{"unknown", "passed", "failed", "timedOut"} {
		t.Run("compile/"+status, func(t *testing.T) {
			r := validReferenceReportFixture(t)
			r.CandidateExecutions[0].Compile = compileStage{State: "executed", Observation: &compileObservation{Status: status, Started: true, CleanupSucceeded: true}}
			if err := validateReferenceReport(r); err == nil {
				t.Fatal("inconsistent compile branch accepted")
			}
		})
	}
}

func TestReferenceProfileEmptyCandidatesAreArrays(t *testing.T) {
	project := bridgeFixture(t, "package sample\n", "package sample\n")
	options := bridgeOptions{projectRoot: project, goBinary: bridgeScript(t, "exit 0"), runnerBinary: bridgeScript(t, `for last; do :; done
printf 'mode: set\n' > "$last"
printf '%s' '`+strings.Replace(lifecycleTimedOut, "timedOut", "passed", 1)+`'`), referenceRunnerBinary: referenceRecorder(t, filepath.Join(t.TempDir(), "calls")), referenceRunID: strings.Repeat("0", 32), timeout: 1000000000, sources: []string{"value.go"}}
	r, code, err := executeReference(context.Background(), options)
	if err != nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
	var out bytes.Buffer
	if err := encodeReferenceReport(context.Background(), &out, r); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"candidates", "outcomes", "candidateExecutions"} {
		if string(raw[key]) != "[]" {
			t.Errorf("%s=%s", key, raw[key])
		}
	}
}

func TestReferenceProfileAcceptsRequiredPair(t *testing.T) {
	project := bridgeFixture(t, "package sample\n", "package sample\n")
	binary := bridgeScript(t, "exit 0")
	_, err := parseOptions([]string{
		"--project-root", project,
		"--go-binary", binary,
		"--runner-binary", binary,
		"--reference-runner-binary=" + binary,
		"--reference-run-id=0123456789abcdef0123456789abcdef",
		"--timeout-ms", "1000",
		"--source", "value.go",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReferenceProfileRejectsInvalidPairBeforeFilesystemAccess(t *testing.T) {
	cases := [][]string{
		{"--reference-runner-binary", "/missing"},
		{"--reference-run-id", "0123456789abcdef0123456789abcdef"},
		{"--reference-runner-binary", ""},
		{"--reference-run-id", ""},
		{"--reference-run-id", "0123456789ABCDEF0123456789abcdef"},
		{"--reference-run-id", "0123456789abcdef0123456789abcde"},
		{"--reference-run-id", "0123456789abcdef0123456789abcdef", "--reference-run-id", "0123456789abcdef0123456789abcdef"},
		{"--reference-runner-binary", "/one", "--reference-runner-binary=/two"},
	}
	for _, selected := range cases {
		t.Run(strings.Join(selected, " "), func(t *testing.T) {
			arguments := []string{"--project-root", filepath.Join(t.TempDir(), "missing"), "--go-binary", "/missing", "--runner-binary", "/missing", "--timeout-ms", "1000", "--source", "value.go"}
			arguments = append(arguments, selected...)
			_, err := parseOptions(arguments)
			if err == nil || strings.Contains(err.Error(), "project root") || strings.Contains(err.Error(), "executable") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestReferenceProfileOmissionKeepsLegacyGoldenBytes(t *testing.T) {
	project := bridgeFixture(t, "package sample\nfunc Positive(x int) bool { return x > 0 }\n", "package sample\n")
	typedRunner := bridgeScript(t, fmt.Sprintf(`while [ "$#" -gt 0 ]; do
  if [ "$1" = "--coverprofile" ]; then shift; printf 'mode: set\n' > "$1"; fi
  shift
done
printf '%%s' '%s'`, strings.Replace(lifecycleTimedOut, "timedOut", "passed", 1)))
	var stdout, stderr bytes.Buffer
	exit := run([]string{"--project-root", project, "--go-binary", bridgeScript(t, "exit 0"), "--runner-binary", typedRunner, "--timeout-ms", "1000", "--source", "value.go"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	const want = `{"schemaVersion":"sentinel-mutate4go-report-v1","backendName":"mutate4go","backendCommit":"9016c7adafc1c7e282b5e27768e732e477713af8","sourceInventory":[{"path":"value.go","sha256":"e7fbd9c45c3e3c14fd99b20b4b0cf612e7e033272287fb822ffdd3f1e99f4927","candidateCount":2}],"candidates":[{"id":"1965270bbeaeeac59ef37daabee4c677173437d9ae49cd01e02787f14379b683","sourceFile":"value.go","line":2,"column":38,"operator":"> -> >="},{"id":"b569cf63999c412c2d5636ff107dac198df50688b84382ecab6b7c8082e39756","sourceFile":"value.go","line":2,"column":40,"operator":"0 -> 1"}],"outcomes":[{"candidateId":"1965270bbeaeeac59ef37daabee4c677173437d9ae49cd01e02787f14379b683","status":"uncovered","durationNanos":0},{"candidateId":"b569cf63999c412c2d5636ff107dac198df50688b84382ecab6b7c8082e39756","status":"uncovered","durationNanos":0}]}` + "\n"
	if stdout.String() != want {
		t.Fatalf("legacy bytes changed:\n%s", stdout.String())
	}
}
