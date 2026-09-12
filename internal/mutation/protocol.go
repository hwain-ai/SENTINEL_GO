package mutation

const (
	Mutate4GoCommit   = "9016c7adafc1c7e282b5e27768e732e477713af8"
	BridgeSchema      = "sentinel-mutate4go-report-v1"
	maximumSafeCount  = int64(9007199254740991)
	sha256DigestBytes = 32
)

// Status is the normalized cross-language mutant state.
type Status string

const (
	Killed       Status = "killed"
	Survived     Status = "survived"
	Uncovered    Status = "uncovered"
	TimedOut     Status = "timedOut"
	CompileError Status = "compileError"
	RuntimeError Status = "runtimeError"
	Pending      Status = "pending"
	Ignored      Status = "ignored"
	ToolError    Status = "toolError"
)

var statuses = []Status{
	Killed,
	Survived,
	Uncovered,
	TimedOut,
	CompileError,
	RuntimeError,
	Pending,
	Ignored,
	ToolError,
}

// Candidate is one mutation site reported before any mutant result.
type Candidate struct {
	ID         string `json:"id"`
	SourceFile string `json:"sourceFile"`
	Line       int64  `json:"line"`
	Column     int64  `json:"column"`
	Operator   string `json:"operator"`
}

// RawOutcome is the backend result for exactly one candidate.
type RawOutcome struct {
	CandidateID   string `json:"candidateId"`
	Status        string `json:"status"`
	DurationNanos int64  `json:"durationNanos"`
}

// SourceInventory proves which requested production files were included in
// candidate discovery, including files that legitimately have zero sites.
type SourceInventory struct {
	Path           string `json:"path"`
	SHA256         string `json:"sha256"`
	CandidateCount int64  `json:"candidateCount"`
}

// BridgeReport is the complete machine-readable mutate4go adapter report.
type BridgeReport struct {
	SchemaVersion   string            `json:"schemaVersion"`
	BackendName     string            `json:"backendName"`
	BackendCommit   string            `json:"backendCommit"`
	SourceInventory []SourceInventory `json:"sourceInventory"`
	Candidates      []Candidate       `json:"candidates"`
	Outcomes        []RawOutcome      `json:"outcomes"`
}

// MutantRecord is the normalized result consumed by the strict gate.
type MutantRecord struct {
	CandidateID string `json:"candidateId"`
	Status      Status `json:"status"`
}

// ExactRate is a reduced exact fraction and its canonical decimal rendering.
type ExactRate struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
	Decimal     string `json:"decimal"`
}

// GateResult is the strict killed-only decision.
type GateResult struct {
	Pass                  bool             `json:"pass"`
	InScope               int64            `json:"inScope"`
	UnauthorizedExclusion int64            `json:"unauthorizedExclusion"`
	Counts                map[Status]int64 `json:"counts"`
	KillRate              *ExactRate       `json:"killRate,omitempty"`
}
