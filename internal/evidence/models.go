package evidence

import "time"

const (
	RunSchemaVersion      = "sentinel-local-run-v1"
	projectSchemaVersion  = "sentinel-project-state-v1"
	stateVersion          = "state-v1"
	startedSchemaVersion  = "run-started-v1"
	eventSchemaVersion    = "finding-event-v1"
	evidenceSchemaVersion = "sentinel-evidence-v1"
	fingerprintVersion    = "sentinel-fingerprint-v1"
	specVersion           = "1.0.0"
	sequenceVersion       = "commit-sequence-v1"
)

// Finding contains only a project-keyed fingerprint in persisted evidence.
// RawIdentity exists solely to make an accidental privacy boundary crossing fail.
type Finding struct {
	Fingerprint string `json:"fingerprint"`
	Component   string `json:"component"`
	Kind        string `json:"kind"`
	RawIdentity string `json:"-"`
}

// RISK(breaking): 이 구조의 JSON field는 history 소비자와 영속 schema가 함께 사용한다.
// RunRecord is the history view reconstructed from an authenticated bundle.
type RunRecord struct {
	Certification      bool       `json:"certification"`
	Command            string     `json:"command"`
	CommitSequence     string     `json:"commitSequence"`
	CommittedAtUTC     time.Time  `json:"committedAtUtc"`
	CompletedAtUTC     time.Time  `json:"completedAtUtc"`
	Components         Components `json:"components"`
	CorrelationID      string     `json:"correlationId"`
	DiagnosticCodes    []string   `json:"diagnosticCodes"`
	ExitCode           int        `json:"exitCode"`
	FingerprintVersion string     `json:"fingerprintVersion"`
	KeyEpoch           uint64     `json:"keyEpoch"`
	Language           string     `json:"language"`
	Mode               string     `json:"mode"`
	ObservationSource  string     `json:"observationSource"`
	OccurredAtUTC      time.Time  `json:"occurredAtUtc"`
	RunID              string     `json:"runId"`
	SchemaVersion      string     `json:"schemaVersion"`
	SourceRunID        *string    `json:"sourceRunId"`
	SpecVersion        string     `json:"specVersion"`
	TerminalStatus     string     `json:"terminalStatus"`
	Findings           []Finding  `json:"findings"`
}

// Components is the exact command-specific quality summary persisted in evidence.
type Components struct {
	Crap     *CrapComponent     `json:"crap,omitempty"`
	Mutation *MutationComponent `json:"mutation,omitempty"`
}

type CrapComponent struct {
	CallableCount  uint64 `json:"callableCount"`
	MaxDenominator string `json:"maxDenominator"`
	MaxNumerator   string `json:"maxNumerator"`
	Pass           bool   `json:"pass"`
	UnknownCount   uint64 `json:"unknownCount"`
}

type MutationComponent struct {
	CompileError          uint64 `json:"compileError"`
	Ignored               uint64 `json:"ignored"`
	InScope               uint64 `json:"inScope"`
	Killed                uint64 `json:"killed"`
	Pass                  bool   `json:"pass"`
	Pending               uint64 `json:"pending"`
	RuntimeError          uint64 `json:"runtimeError"`
	Survived              uint64 `json:"survived"`
	TimedOut              uint64 `json:"timedOut"`
	ToolError             uint64 `json:"toolError"`
	UnauthorizedExclusion uint64 `json:"unauthorizedExclusion"`
	Uncovered             uint64 `json:"uncovered"`
}

type projectState struct {
	CleanupLeaseKey    string `json:"cleanupLeaseKey"`
	FingerprintHMACKey string `json:"fingerprintHmacKey"`
	KeyEpoch           uint64 `json:"keyEpoch"`
	ProjectIdentifier  string `json:"projectIdentifier"`
	SchemaVersion      string `json:"schemaVersion"`
	StateVersion       string `json:"stateVersion"`
}

type startedRecord struct {
	Command       string `json:"command"`
	RunID         string `json:"runId"`
	SchemaVersion string `json:"schemaVersion"`
	StartedAtUTC  string `json:"startedAtUtc"`
}

type eventBody struct {
	CommitSequence   string `json:"commitSequence"`
	Component        string `json:"component"`
	EventID          string `json:"eventId"`
	Fingerprint      string `json:"fingerprint"`
	FingerprintEpoch uint64 `json:"fingerprintEpoch"`
	Kind             string `json:"kind"`
	ObservedAtUTC    string `json:"observedAtUtc"`
	RunID            string `json:"runId"`
	SchemaVersion    string `json:"schemaVersion"`
}

type eventRecord struct {
	CommitSequence   string `json:"commitSequence"`
	Component        string `json:"component"`
	EventID          string `json:"eventId"`
	Fingerprint      string `json:"fingerprint"`
	FingerprintEpoch uint64 `json:"fingerprintEpoch"`
	HMACSHA256       string `json:"hmacSha256"`
	Kind             string `json:"kind"`
	ObservedAtUTC    string `json:"observedAtUtc"`
	RunID            string `json:"runId"`
	SchemaVersion    string `json:"schemaVersion"`
}

type eventManifestEntry struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
}

type evidenceBody struct {
	Certification      bool                 `json:"certification"`
	Command            string               `json:"command"`
	CommitSequence     string               `json:"commitSequence"`
	CommittedAtUTC     string               `json:"committedAtUtc"`
	CompletedAtUTC     string               `json:"completedAtUtc"`
	Components         Components           `json:"components"`
	CorrelationID      string               `json:"correlationId"`
	DiagnosticCodes    []string             `json:"diagnosticCodes"`
	EventCount         uint64               `json:"eventCount"`
	Events             []eventManifestEntry `json:"events"`
	ExitCode           int                  `json:"exitCode"`
	FingerprintVersion string               `json:"fingerprintVersion"`
	KeyEpoch           uint64               `json:"keyEpoch"`
	Language           string               `json:"language"`
	Mode               string               `json:"mode"`
	ObservationSource  string               `json:"observationSource"`
	ProjectStateHMAC   string               `json:"projectStateHmac"`
	RunID              string               `json:"runId"`
	SchemaVersion      string               `json:"schemaVersion"`
	SourceRunID        *string              `json:"sourceRunId"`
	SpecVersion        string               `json:"specVersion"`
	StartedAtUTC       string               `json:"startedAtUtc"`
	StartedSHA256      string               `json:"startedSha256"`
	TerminalStatus     string               `json:"terminalStatus"`
}

type evidenceRecord struct {
	Certification      bool                 `json:"certification"`
	Command            string               `json:"command"`
	CommitSequence     string               `json:"commitSequence"`
	CommittedAtUTC     string               `json:"committedAtUtc"`
	CompletedAtUTC     string               `json:"completedAtUtc"`
	Components         Components           `json:"components"`
	CorrelationID      string               `json:"correlationId"`
	DiagnosticCodes    []string             `json:"diagnosticCodes"`
	EventCount         uint64               `json:"eventCount"`
	Events             []eventManifestEntry `json:"events"`
	ExitCode           int                  `json:"exitCode"`
	FingerprintVersion string               `json:"fingerprintVersion"`
	HMACSHA256         string               `json:"hmacSha256"`
	KeyEpoch           uint64               `json:"keyEpoch"`
	Language           string               `json:"language"`
	Mode               string               `json:"mode"`
	ObservationSource  string               `json:"observationSource"`
	ProjectStateHMAC   string               `json:"projectStateHmac"`
	RunID              string               `json:"runId"`
	SchemaVersion      string               `json:"schemaVersion"`
	SourceRunID        *string              `json:"sourceRunId"`
	SpecVersion        string               `json:"specVersion"`
	StartedAtUTC       string               `json:"startedAtUtc"`
	StartedSHA256      string               `json:"startedSha256"`
	TerminalStatus     string               `json:"terminalStatus"`
}

type sequenceBody struct {
	LastAllocated string `json:"lastAllocated"`
	Version       string `json:"version"`
}

type sequenceRecord struct {
	HMACSHA256    string `json:"hmacSha256"`
	LastAllocated string `json:"lastAllocated"`
	Version       string `json:"version"`
}
