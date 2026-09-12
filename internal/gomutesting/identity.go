package gomutesting

import (
	"encoding/json"

	"github.com/hwain-hwang/sentinel-go/internal/mutation"
	"github.com/hwain-hwang/sentinel-go/internal/runner"
)

// These digests are diagnostic bindings only, never certified gate evidence.
type ExecutionIdentity struct {
	Nonce           string        `json:"nonce"`
	InputSHA256     string        `json:"inputSha256"`
	InventorySHA256 string        `json:"inventorySha256"`
	ResultsSHA256   string        `json:"resultsSha256"`
	FailureSHA256   string        `json:"failureSha256,omitempty"`
	Status          runner.Status `json:"status"`
}

type CandidateReplay struct {
	CandidateID string            `json:"candidateId"`
	First       ExecutionIdentity `json:"first"`
	Second      ExecutionIdentity `json:"second"`
}

func executionIdentity(report runner.Report) ExecutionIdentity {
	identity := ExecutionIdentity{Nonce: report.Nonce, InputSHA256: report.InputSHA256, Status: report.Status}
	if report.Replay != nil {
		identity.InventorySHA256 = report.Replay.InventorySHA256
		identity.ResultsSHA256 = report.Replay.ResultsSHA256
		identity.FailureSHA256 = report.Replay.FailureSHA256
	}
	return identity
}

func planDigest(report Report) string {
	// The closed JSON descriptor contains only strings, integer positions and lists.
	// Source replacement bytes are already bound by candidate IDs and stay private.
	plan := struct {
		Version       string
		Profile       string
		ProjectSHA256 string
		Sources       []mutation.SourceInventory
		Candidates    []Candidate
	}{report.Version, report.Profile, report.ProjectSHA256, report.Sources, report.Candidates}
	payload, _ := json.Marshal(plan)
	return digest(payload)
}
