package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/unclebob/mutate4go/internal/coverage"
)

const referenceReportLimit = 16 * 1024 * 1024

func executeReference(ctx context.Context, options bridgeOptions) (referenceReport, int, error) {
	if err := ctx.Err(); err != nil {
		return referenceReport{}, 6, err
	}
	modulePath, err := readModulePath(filepath.Join(options.projectRoot, "go.mod"))
	if err != nil {
		return referenceReport{}, 5, err
	}
	profile, observedCoverage, err := freshReferenceCoverage(ctx, options)
	if ctx.Err() != nil {
		return referenceReport{}, 6, ctx.Err()
	}
	if err != nil {
		return referenceReport{}, 4, err
	}
	capture := newReferenceCapture()
	controls := make([]referenceReceipt, 0, 2)
	for ordinal := 1; ordinal <= 2; ordinal++ {
		receipt, invokeErr := capture.invoke(ctx, options, "control", "", ordinal, options.timeout)
		if invokeErr != nil {
			return referenceReport{}, 6, invokeErr
		}
		controls = append(controls, receipt)
	}
	if !controlEvidenceValid(controls) {
		return referenceReport{}, 4, fmt.Errorf("reference baseline failed")
	}
	legacy, plan, err := discoverPlan(ctx, options, modulePath, profile)
	if err != nil {
		return referenceReport{}, 6, err
	}
	// Empty inventories remain JSON arrays only in this opt-in contract.
	if legacy.SourceInventory == nil {
		legacy.SourceInventory = []sourceInventory{}
	}
	if legacy.Candidates == nil {
		legacy.Candidates = []machineCandidate{}
	}
	outcomes, executions, err := executeReferencePlan(ctx, options, capture, controls, plan)
	if err != nil {
		return referenceReport{}, 6, err
	}
	report := referenceReport{
		SchemaVersion: "sentinel-mutate4go-reference-report-v1", Profile: "replay-identity-v1", RunID: options.referenceRunID,
		BackendName: "mutate4go", BackendCommit: backendCommit, SourceInventory: legacy.SourceInventory,
		Candidates: legacy.Candidates, Outcomes: outcomes, Coverage: observedCoverage, Controls: controls,
		CandidateExecutions: executions, ReferenceOnly: true, Certified: false,
	}
	if err := validateReferenceReport(report); err != nil {
		return referenceReport{}, 6, err
	}
	if err := ctx.Err(); err != nil {
		return referenceReport{}, 6, err
	}
	return report, 0, nil
}

func freshReferenceCoverage(ctx context.Context, options bridgeOptions) (map[string][]coverage.Segment, referenceCoverage, error) {
	coverageRoot := filepath.Join(options.projectRoot, "target", "sentinel-coverage")
	if err := os.MkdirAll(coverageRoot, 0o700); err != nil {
		return nil, referenceCoverage{}, err
	}
	profilePath := filepath.Join(coverageRoot, "coverage.out")
	report, err := invokeTypedTestsContext(ctx, options, profilePath)
	if err != nil || report.Status != "passed" {
		return nil, referenceCoverage{}, fmt.Errorf("coverage baseline failed")
	}
	payload, err := readBoundedFile(profilePath, referenceReportLimit)
	if err != nil {
		return nil, referenceCoverage{}, fmt.Errorf("coverage profile invalid")
	}
	profile, err := coverage.ParseProfile(bytes.NewReader(payload))
	if err != nil || profile == nil {
		return nil, referenceCoverage{}, fmt.Errorf("coverage profile invalid")
	}
	digest := sha256.Sum256(payload)
	return profile, referenceCoverage{ProducerSchema: report.SchemaVersion, Status: report.Status, ProfileSHA256: hex.EncodeToString(digest[:])}, nil
}

func readBoundedFile(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(payload) > limit {
		return nil, fmt.Errorf("bounded read failed")
	}
	return payload, nil
}

// RISK(side-effect): every captured call is an actual guarded runner execution.
// The explicit profile pair is the only route here, and no legacy fallback exists.
func (capture *referenceCapture) invoke(ctx context.Context, options bridgeOptions, phase, candidateID string, ordinal int, timeout time.Duration) (referenceReceipt, error) {
	if err := ctx.Err(); err != nil {
		return referenceReceipt{}, err
	}
	requestID, err := capture.nextRequestID()
	if err != nil {
		return referenceReceipt{}, err
	}
	capture.invocations = append(capture.invocations, capturedInvocation{phase: phase, candidateID: candidateID, ordinal: ordinal, timeout: timeout})
	childContext, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()
	command := exec.CommandContext(childContext, options.referenceRunnerBinary,
		"--project-root", options.projectRoot,
		"--go-binary", options.goBinary,
		"--request-id", requestID,
		"--timeout-ms", strconv.FormatInt(timeout.Milliseconds(), 10),
	)
	command.Dir = options.projectRoot
	command.Env = os.Environ()
	command.Stdin = nil
	var stdout referenceOutput
	command.Stdout = &stdout
	command.Stderr = io.Discard
	runErr, cleanupErr := capture.supervise(command, syscall.SIGTERM, 3*time.Second)
	if ctx.Err() != nil {
		return referenceReceipt{}, ctx.Err()
	}
	if runErr != nil || cleanupErr != nil || childContext.Err() != nil || stdout.exceeded {
		return referenceReceipt{}, fmt.Errorf("reference runner execution failed")
	}
	receipt, err := decodeReferenceReceipt(stdout.buffer.Bytes())
	if err != nil || receipt.RequestID != requestID || receipt.TimeoutMilliseconds != timeout.Milliseconds() {
		return referenceReceipt{}, fmt.Errorf("reference runner receipt failed")
	}
	if err := capture.acceptNonce(receipt.Nonce); err != nil {
		return referenceReceipt{}, err
	}
	return receipt, nil
}

type referenceOutput struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (output *referenceOutput) Write(payload []byte) (int, error) {
	remaining := referenceRunnerOutputLimit - output.buffer.Len()
	if len(payload) > remaining {
		output.exceeded = true
		if remaining > 0 {
			_, _ = output.buffer.Write(payload[:remaining])
		}
		return len(payload), nil
	}
	return output.buffer.Write(payload)
}

func executeReferencePlan(ctx context.Context, options bridgeOptions, capture *referenceCapture, controls []referenceReceipt, plan []plannedCandidate) ([]machineOutcome, []candidateExecution, error) {
	outcomes := make([]machineOutcome, 0, len(plan))
	executions := make([]candidateExecution, 0, len(plan))
	for _, candidate := range plan {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		outcome, execution, err := executeReferenceCandidate(ctx, options, capture, controls, candidate)
		if err != nil {
			return nil, nil, err
		}
		outcomes = append(outcomes, outcome)
		executions = append(executions, execution)
	}
	return outcomes, executions, nil
}

func executeReferenceCandidate(ctx context.Context, options bridgeOptions, capture *referenceCapture, controls []referenceReceipt, planned plannedCandidate) (machineOutcome, candidateExecution, error) {
	if err := ctx.Err(); err != nil {
		return machineOutcome{}, candidateExecution{}, err
	}
	notRun := func(reason string) []replayStage {
		return []replayStage{{Ordinal: 1, State: "notExecuted", Reason: reason}, {Ordinal: 2, State: "notExecuted", Reason: reason}}
	}
	if !planned.covered {
		return machineOutcome{CandidateID: planned.candidate.ID, Status: "uncovered"}, candidateExecution{
			CandidateID: planned.candidate.ID, Compile: compileStage{State: "notExecuted", Reason: "uncovered"}, Replays: notRun("uncovered"),
		}, nil
	}
	var execution candidateExecution
	outcome, err := executeCandidateWith(ctx, options, planned, func(selected bridgeOptions) (string, error) {
		var observed machineOutcome
		var observeErr error
		observed, execution, observeErr = observeReferenceCandidate(ctx, selected, capture, controls, planned.candidate.ID, selected.timeout)
		return observed.Status, observeErr
	})
	if err != nil {
		return machineOutcome{}, candidateExecution{}, err
	}
	return outcome, execution, nil
}

func observeReferenceCandidate(ctx context.Context, options bridgeOptions, capture *referenceCapture, controls []referenceReceipt, candidateID string, timeout time.Duration) (machineOutcome, candidateExecution, error) {
	compile, err := runGoObservation(ctx, options, timeout, "test", "./...", "-run=^$", "-count=1")
	if err != nil {
		return machineOutcome{}, candidateExecution{}, err
	}
	execution := candidateExecution{CandidateID: candidateID, Compile: compileStage{State: "executed", Observation: &compile}}
	if compile.Status != "passed" {
		execution.Replays = []replayStage{{Ordinal: 1, State: "notExecuted", Reason: "compileBlocked"}, {Ordinal: 2, State: "notExecuted", Reason: "compileBlocked"}}
		status := "compileError"
		if compile.Status == "timedOut" {
			status = "timedOut"
		}
		return machineOutcome{CandidateID: candidateID, Status: status}, execution, nil
	}
	receipts := make([]referenceReceipt, 0, 2)
	for ordinal := 1; ordinal <= 2; ordinal++ {
		receipt, err := capture.invoke(ctx, options, "candidate", candidateID, ordinal, timeout)
		if err != nil {
			return machineOutcome{}, candidateExecution{}, err
		}
		receipts = append(receipts, receipt)
		execution.Replays = append(execution.Replays, replayStage{Ordinal: ordinal, State: "executed", Receipt: &receipts[len(receipts)-1]})
	}
	return machineOutcome{CandidateID: candidateID, Status: referenceReplayOutcome(controls, receipts)}, execution, nil
}

func validateReferenceReport(report referenceReport) error {
	if report.SchemaVersion != "sentinel-mutate4go-reference-report-v1" || report.Profile != "replay-identity-v1" ||
		!validLowerHex(report.RunID, 32) || report.BackendName != "mutate4go" || report.BackendCommit != backendCommit ||
		!report.ReferenceOnly || report.Certified || report.Coverage.ProducerSchema != "sentinel-go-typed-runner-v1" ||
		report.Coverage.Status != "passed" || !validLowerHex(report.Coverage.ProfileSHA256, 64) || !controlEvidenceValid(report.Controls) ||
		report.SourceInventory == nil || report.Candidates == nil || report.Outcomes == nil || report.CandidateExecutions == nil ||
		len(report.Candidates) != len(report.Outcomes) || len(report.Candidates) != len(report.CandidateExecutions) {
		return fmt.Errorf("reference report is invalid")
	}
	requests, nonces := make(map[string]bool), make(map[string]bool)
	checkReceipt := func(receipt referenceReceipt) error {
		if err := validateReferenceReceipt(receipt); err != nil {
			return err
		}
		if requests[receipt.RequestID] || nonces[receipt.Nonce] {
			return fmt.Errorf("duplicate reference identity")
		}
		requests[receipt.RequestID], nonces[receipt.Nonce] = true, true
		return nil
	}
	for _, control := range report.Controls {
		if err := checkReceipt(control); err != nil {
			return err
		}
	}
	position := 0
	for index, source := range report.SourceInventory {
		if !validRelativeSource(source.Path) || !validLowerHex(source.SHA256, 64) || source.CandidateCount < 0 ||
			(index > 0 && report.SourceInventory[index-1].Path >= source.Path) || source.CandidateCount > int64(len(report.Candidates)-position) {
			return fmt.Errorf("reference source correspondence failed")
		}
		end := position + int(source.CandidateCount)
		for ; position < end; position++ {
			if report.Candidates[position].SourceFile != source.Path {
				return fmt.Errorf("reference source correspondence failed")
			}
		}
	}
	if position != len(report.Candidates) {
		return fmt.Errorf("reference report correspondence failed")
	}
	ids := make(map[string]bool)
	for index, candidate := range report.Candidates {
		outcome, execution := report.Outcomes[index], report.CandidateExecutions[index]
		if !validLowerHex(candidate.ID, 64) || ids[candidate.ID] || candidate.Line < 1 || candidate.Column < 1 || candidate.Operator == "" ||
			outcome.DurationNanos < 0 || outcome.DurationNanos > 9_007_199_254_740_991 ||
			outcome.CandidateID != candidate.ID || execution.CandidateID != candidate.ID || len(execution.Replays) != 2 ||
			execution.Replays[0].Ordinal != 1 || execution.Replays[1].Ordinal != 2 {
			return fmt.Errorf("reference report correspondence failed")
		}
		ids[candidate.ID] = true
		reason, expectedOutcome := "uncovered", "uncovered"
		if execution.Compile.State == "executed" {
			observation := execution.Compile.Observation
			if execution.Compile.Reason != "" || observation == nil || !observation.Started || !observation.CleanupSucceeded || observation.ExitCode < -1 {
				return fmt.Errorf("reference compile stage failed")
			}
			reason = "compileBlocked"
			switch observation.Status {
			case "passed":
				if observation.ExitCode != 0 || observation.DeadlineExceeded {
					return fmt.Errorf("reference compile stage failed")
				}
				reason = ""
			case "failed":
				if observation.ExitCode == 0 || observation.DeadlineExceeded {
					return fmt.Errorf("reference compile stage failed")
				}
				expectedOutcome = "compileError"
			case "timedOut":
				if !observation.DeadlineExceeded {
					return fmt.Errorf("reference compile stage failed")
				}
				expectedOutcome = "timedOut"
			default:
				return fmt.Errorf("reference compile stage failed")
			}
		} else if execution.Compile.State != "notExecuted" || execution.Compile.Reason != "uncovered" || execution.Compile.Observation != nil {
			return fmt.Errorf("reference compile stage failed")
		} else if outcome.DurationNanos != 0 {
			return fmt.Errorf("reference uncovered duration failed")
		}
		receipts := make([]referenceReceipt, 0, 2)
		for _, replay := range execution.Replays {
			if reason == "" {
				if replay.State != "executed" || replay.Reason != "" || replay.Receipt == nil {
					return fmt.Errorf("reference replay stage failed")
				}
				if err := checkReceipt(*replay.Receipt); err != nil {
					return err
				}
				receipts = append(receipts, *replay.Receipt)
			} else if replay.State != "notExecuted" || replay.Reason != reason || replay.Receipt != nil {
				return fmt.Errorf("reference replay stage failed")
			}
		}
		if reason == "" {
			expectedOutcome = referenceReplayOutcome(report.Controls, receipts)
		}
		if outcome.Status != expectedOutcome {
			return fmt.Errorf("reference outcome correspondence failed")
		}
	}
	return nil
}

type boundedReportWriter struct {
	buffer bytes.Buffer
	limit  int
}

func (writer *boundedReportWriter) Write(payload []byte) (int, error) {
	remaining := writer.limit - writer.buffer.Len()
	if len(payload) > remaining {
		if remaining > 0 {
			_, _ = writer.buffer.Write(payload[:remaining])
		}
		return remaining, fmt.Errorf("reference report exceeds limit")
	}
	return writer.buffer.Write(payload)
}

func encodeReferenceReport(ctx context.Context, output io.Writer, report referenceReport) error {
	if err := validateReferenceReport(report); err != nil {
		return err
	}
	staging := &boundedReportWriter{limit: referenceReportLimit}
	encoder := json.NewEncoder(staging)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(report); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	payload := staging.buffer.Bytes()
	written, err := output.Write(payload)
	if err != nil || written != len(payload) {
		return io.ErrShortWrite
	}
	return nil
}
