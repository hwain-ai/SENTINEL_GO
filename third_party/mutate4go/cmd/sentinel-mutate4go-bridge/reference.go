package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

type referenceCoverage struct {
	ProducerSchema string `json:"producerSchema"`
	Status         string `json:"status"`
	ProfileSHA256  string `json:"profileSha256"`
}

type compileObservation struct {
	Status           string `json:"status"`
	Started          bool   `json:"started"`
	ExitCode         int    `json:"exitCode"`
	DeadlineExceeded bool   `json:"deadlineExceeded"`
	CleanupSucceeded bool   `json:"cleanupSucceeded"`
}

type compileStage struct {
	State       string              `json:"state"`
	Reason      string              `json:"reason"`
	Observation *compileObservation `json:"observation"`
}

type replayStage struct {
	Ordinal int               `json:"ordinal"`
	State   string            `json:"state"`
	Reason  string            `json:"reason"`
	Receipt *referenceReceipt `json:"receipt"`
}

type candidateExecution struct {
	CandidateID string        `json:"candidateId"`
	Compile     compileStage  `json:"compile"`
	Replays     []replayStage `json:"replays"`
}

type referenceReport struct {
	SchemaVersion       string               `json:"schemaVersion"`
	Profile             string               `json:"profile"`
	RunID               string               `json:"runId"`
	BackendName         string               `json:"backendName"`
	BackendCommit       string               `json:"backendCommit"`
	SourceInventory     []sourceInventory    `json:"sourceInventory"`
	Candidates          []machineCandidate   `json:"candidates"`
	Outcomes            []machineOutcome     `json:"outcomes"`
	Coverage            referenceCoverage    `json:"coverage"`
	Controls            []referenceReceipt   `json:"controls"`
	CandidateExecutions []candidateExecution `json:"candidateExecutions"`
	ReferenceOnly       bool                 `json:"referenceOnly"`
	Certified           bool                 `json:"certified"`
}

type capturedInvocation struct {
	phase       string
	candidateID string
	ordinal     int
	timeout     time.Duration
}

type referenceCapture struct {
	random      io.Reader
	supervise   func(*exec.Cmd, syscall.Signal, time.Duration) (error, error)
	requestIDs  map[string]struct{}
	nonces      map[string]struct{}
	invocations []capturedInvocation
}

func newReferenceCapture() *referenceCapture {
	// RISK(race): identity sets and supervision belong to this run only.
	return &referenceCapture{random: rand.Reader, supervise: runCommandGroup, requestIDs: make(map[string]struct{}), nonces: make(map[string]struct{})}
}

func (capture *referenceCapture) nextRequestID() (string, error) {
	payload := make([]byte, 16)
	if _, err := io.ReadFull(capture.random, payload); err != nil {
		return "", fmt.Errorf("reference request identity failed")
	}
	requestID := hex.EncodeToString(payload)
	if _, exists := capture.requestIDs[requestID]; exists {
		return "", fmt.Errorf("duplicate reference request identity")
	}
	capture.requestIDs[requestID] = struct{}{}
	return requestID, nil
}

func (capture *referenceCapture) acceptNonce(nonce string) error {
	if _, exists := capture.nonces[nonce]; exists {
		return fmt.Errorf("duplicate reference runner nonce")
	}
	capture.nonces[nonce] = struct{}{}
	return nil
}

func controlEvidenceValid(controls []referenceReceipt) bool {
	if len(controls) != 2 || controls[0].Status != "passed" || controls[1].Status != "passed" || controls[0].Replay == nil || controls[1].Replay == nil {
		return false
	}
	first, second := controls[0], controls[1]
	return first.InputSHA256 == second.InputSHA256 && first.InventoryCount == second.InventoryCount &&
		first.Replay.InventorySHA256 == second.Replay.InventorySHA256 && first.Replay.ResultsSHA256 == second.Replay.ResultsSHA256
}

func referenceReplayOutcome(controls []referenceReceipt, replays []referenceReceipt) string {
	if len(controls) != 2 || len(replays) != 2 || replays[0].Replay == nil || replays[1].Replay == nil {
		return "toolError"
	}
	first, second, control := replays[0], replays[1], controls[0]
	if first.InventoryCount != control.InventoryCount || second.InventoryCount != control.InventoryCount ||
		first.Replay.InventorySHA256 != control.Replay.InventorySHA256 || second.Replay.InventorySHA256 != control.Replay.InventorySHA256 ||
		first.InputSHA256 != second.InputSHA256 || first.Status != second.Status ||
		first.Replay.ResultsSHA256 != second.Replay.ResultsSHA256 || first.Replay.FailureSHA256 != second.Replay.FailureSHA256 {
		return "toolError"
	}
	switch first.Status {
	case "passed":
		return "survived"
	case "assertionFailure":
		return "killed"
	case "compileError", "runtimeError", "timedOut", "toolError":
		return first.Status
	default:
		return "toolError"
	}
}
