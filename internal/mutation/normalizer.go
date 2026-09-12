package mutation

import (
	"encoding/hex"
	"fmt"
	"path"
	"strings"
)

// Normalize verifies report identity and candidate/result completeness before
// converting raw backend states into the shared state vocabulary.
func Normalize(report BridgeReport) ([]MutantRecord, error) {
	if err := validateReportIdentity(report); err != nil {
		return nil, err
	}
	inventory, err := indexSourceInventory(report.SourceInventory)
	if err != nil {
		return nil, err
	}
	candidates, err := indexCandidates(report.Candidates)
	if err != nil {
		return nil, err
	}
	if err := validateCandidateInventory(candidates, inventory); err != nil {
		return nil, err
	}
	records, seen, err := normalizeOutcomes(report.Outcomes, candidates)
	if err != nil {
		return nil, err
	}
	if len(seen) != len(candidates) {
		return nil, fmt.Errorf("backendReportIncomplete: candidates=%d outcomes=%d", len(candidates), len(seen))
	}
	return records, nil
}

func indexSourceInventory(entries []SourceInventory) (map[string]SourceInventory, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("backendSourceInventoryMissing")
	}
	indexed := make(map[string]SourceInventory, len(entries))
	for _, entry := range entries {
		if err := validateSourceInventory(entry); err != nil {
			return nil, err
		}
		if _, duplicate := indexed[entry.Path]; duplicate {
			return nil, fmt.Errorf("backendSourceInventoryDuplicate: %s", entry.Path)
		}
		indexed[entry.Path] = entry
	}
	return indexed, nil
}

func validateSourceInventory(entry SourceInventory) error {
	if !canonicalProductionPath(entry.Path) {
		return fmt.Errorf("backendSourceInventoryPathInvalid")
	}
	if !validLowerHexDigest(entry.SHA256) {
		return fmt.Errorf("backendSourceInventoryDigestInvalid")
	}
	if entry.CandidateCount < 0 || entry.CandidateCount > maximumSafeCount {
		return fmt.Errorf("backendSourceInventoryCountInvalid")
	}
	return nil
}

func validLowerHexDigest(value string) bool {
	digest, err := hex.DecodeString(value)
	return err == nil && len(digest) == sha256DigestBytes && strings.ToLower(value) == value
}

func canonicalProductionPath(value string) bool {
	return value != "" && !strings.Contains(value, `\`) && !path.IsAbs(value) && path.Clean(value) == value &&
		!strings.HasPrefix(value, "../") && value != ".." && strings.HasSuffix(value, ".go") && !strings.HasSuffix(value, "_test.go")
}

func validateCandidateInventory(candidates map[string]Candidate, inventory map[string]SourceInventory) error {
	actualCounts := make(map[string]int64, len(inventory))
	for _, candidate := range candidates {
		if _, exists := inventory[candidate.SourceFile]; !exists {
			return fmt.Errorf("backendCandidateSourceMissing: %s", candidate.SourceFile)
		}
		actualCounts[candidate.SourceFile]++
	}
	for source, expected := range inventory {
		if actualCounts[source] != expected.CandidateCount {
			return fmt.Errorf("backendCandidateCountMismatch: %s", source)
		}
	}
	return nil
}

func validateReportIdentity(report BridgeReport) error {
	if report.SchemaVersion != BridgeSchema {
		return fmt.Errorf("backendReportSchemaMismatch")
	}
	if report.BackendName != "mutate4go" || report.BackendCommit != Mutate4GoCommit {
		return fmt.Errorf("backendIdentityMismatch")
	}
	return nil
}

func indexCandidates(candidates []Candidate) (map[string]Candidate, error) {
	indexed := make(map[string]Candidate, len(candidates))
	for _, candidate := range candidates {
		if err := validateCandidate(candidate); err != nil {
			return nil, err
		}
		if _, exists := indexed[candidate.ID]; exists {
			return nil, fmt.Errorf("backendCandidateDuplicate: %s", candidate.ID)
		}
		indexed[candidate.ID] = candidate
	}
	return indexed, nil
}

func validateCandidate(candidate Candidate) error {
	if !completeCandidateDescriptor(candidate) {
		return fmt.Errorf("backendCandidateInvalid")
	}
	if !validCandidatePosition(candidate.Line, candidate.Column) {
		return fmt.Errorf("backendCandidatePositionInvalid")
	}
	return nil
}

func completeCandidateDescriptor(candidate Candidate) bool {
	return candidate.ID != "" && candidate.SourceFile != "" && candidate.Operator != ""
}

func validCandidatePosition(line, column int64) bool {
	return line >= 1 && line <= maximumSafeCount && column >= 1 && column <= maximumSafeCount
}

func normalizeOutcomes(outcomes []RawOutcome, candidates map[string]Candidate) ([]MutantRecord, map[string]struct{}, error) {
	records := make([]MutantRecord, 0, len(outcomes))
	seen := make(map[string]struct{}, len(outcomes))
	for _, outcome := range outcomes {
		status, err := normalizeStatus(outcome.Status)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := candidates[outcome.CandidateID]; !exists {
			return nil, nil, fmt.Errorf("backendOutcomeWithoutCandidate: %s", outcome.CandidateID)
		}
		if _, duplicate := seen[outcome.CandidateID]; duplicate {
			return nil, nil, fmt.Errorf("backendOutcomeDuplicate: %s", outcome.CandidateID)
		}
		if outcome.DurationNanos < 0 || outcome.DurationNanos > maximumSafeCount {
			return nil, nil, fmt.Errorf("backendOutcomeDurationInvalid")
		}
		seen[outcome.CandidateID] = struct{}{}
		records = append(records, MutantRecord{CandidateID: outcome.CandidateID, Status: status})
	}
	return records, seen, nil
}

func normalizeStatus(raw string) (Status, error) {
	status := Status(raw)
	for _, allowed := range statuses {
		if status == allowed {
			return status, nil
		}
	}
	return "", fmt.Errorf("backendRawStatusUnknown: %s", raw)
}
