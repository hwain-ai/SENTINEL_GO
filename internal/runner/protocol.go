// Package runner provides structured Go test events for mutation decisions.
// It never classifies failures from human-readable test output.
package runner

import "time"

const RunnerSchema = "sentinel-go-typed-runner-v1"

type Status string

const (
	StatusPassed           Status = "passed"
	StatusAssertionFailure Status = "assertionFailure"
	StatusRuntimeError     Status = "runtimeError"
	StatusCompileError     Status = "compileError"
	StatusTimedOut         Status = "timedOut"
	StatusToolError        Status = "toolError"
)

type TestKind string

const (
	KindTest    TestKind = "test"
	KindFuzz    TestKind = "fuzz"
	KindExample TestKind = "example"
)

type EventKind string

const (
	EventStart            EventKind = "start"
	EventNormalReturn     EventKind = "normalReturn"
	EventAssertionFailure EventKind = "assertionFailure"
	EventPanic            EventKind = "panic"
	EventMainStart        EventKind = "mainStart"
	EventMainRunReturned  EventKind = "mainRunReturned"
	EventMainNormalReturn EventKind = "mainNormalReturn"
	EventAssertionSite    EventKind = "assertionSite"
)

type OfficialAction string

const (
	OfficialRun  OfficialAction = "run"
	OfficialPass OfficialAction = "pass"
	OfficialFail OfficialAction = "fail"
	OfficialSkip OfficialAction = "skip"
)

type InventoryEntry struct {
	ID   string   `json:"id"`
	Kind TestKind `json:"kind"`
}

type PrivateEvent struct {
	Nonce  string    `json:"nonce"`
	TestID string    `json:"testId,omitempty"`
	Kind   EventKind `json:"kind"`
	SiteID string    `json:"siteId,omitempty"`
}

type OfficialEvent struct {
	TestID string         `json:"testId"`
	Action OfficialAction `json:"action"`
}

type Request struct {
	ProjectRoot   string
	GoBinary      string
	Timeout       time.Duration
	CoverProfile  string
	CaptureReplay bool
}

type Report struct {
	SchemaVersion  string `json:"schemaVersion"`
	Nonce          string `json:"nonce"`
	Status         Status `json:"status"`
	InventoryCount int    `json:"inventoryCount"`
	// Internal opt-in identity is not a change to the public v1 JSON protocol.
	Replay      *ReplayIdentity `json:"-"`
	InputSHA256 string          `json:"-"`
}

type ReplayIdentity struct {
	InventorySHA256 string
	ResultsSHA256   string
	FailureSHA256   string
}
