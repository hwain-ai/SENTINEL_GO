package mutation

import (
	"fmt"
	"math/big"

	"github.com/hwain-hwang/sentinel-go/internal/crap"
)

// Evaluate applies the exact killed-only rule. A zero-mutant run never has a
// fabricated kill rate and never passes.
func Evaluate(records []MutantRecord, unauthorizedExclusion int64) (GateResult, error) {
	counts := make(map[string]int64, len(statuses))
	for _, status := range statuses {
		counts[string(status)] = 0
	}
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if err := countRecord(record, seen, counts); err != nil {
			return GateResult{}, err
		}
	}
	return EvaluateCounts(counts, int64(len(records)), unauthorizedExclusion)
}

// EvaluateCounts applies the same gate to an aggregate report without
// allocating one record per mutant at the JSON-safe integer boundary.
func EvaluateCounts(rawCounts map[string]int64, inScope, unauthorizedExclusion int64) (GateResult, error) {
	counts, err := validateCounts(rawCounts)
	if err != nil {
		return GateResult{}, err
	}
	if err := validateGateCounts(counts, inScope, unauthorizedExclusion); err != nil {
		return GateResult{}, err
	}
	result := GateResult{InScope: inScope, UnauthorizedExclusion: unauthorizedExclusion, Counts: counts}
	if result.InScope == 0 {
		return result, nil
	}
	if err := populateKillRate(&result); err != nil {
		return GateResult{}, err
	}
	result.Pass = unauthorizedExclusion == 0 && counts[Killed] == result.InScope
	return result, nil
}

func validateGateCounts(counts map[Status]int64, inScope, unauthorizedExclusion int64) error {
	if inScope < 0 || inScope > maximumSafeCount {
		return fmt.Errorf("inScopeOutOfRange")
	}
	if unauthorizedExclusion < 0 || unauthorizedExclusion > maximumSafeCount {
		return fmt.Errorf("unauthorizedExclusionOutOfRange")
	}
	if sumCounts(counts) != inScope {
		return fmt.Errorf("stateCountMismatch")
	}
	return nil
}

func populateKillRate(result *GateResult) error {
	rate, err := exactKillRate(result.Counts[Killed], result.InScope)
	if err != nil {
		return err
	}
	result.KillRate = rate
	return nil
}

func validateCounts(raw map[string]int64) (map[Status]int64, error) {
	if err := validateCountShape(raw); err != nil {
		return nil, err
	}
	counts := emptyCounts()
	for name, value := range raw {
		status, err := validateCountEntry(name, value)
		if err != nil {
			return nil, err
		}
		counts[status] = value
	}
	return counts, nil
}

func validateCountShape(raw map[string]int64) error {
	if len(raw) == len(statuses) {
		return nil
	}
	for name := range raw {
		if !knownStatusName(name) {
			return fmt.Errorf("unknownMutationState")
		}
	}
	return fmt.Errorf("missingMutationState")
}

func validateCountEntry(name string, value int64) (Status, error) {
	status, err := normalizeStatus(name)
	if err != nil {
		return "", fmt.Errorf("unknownMutationState")
	}
	if value < 0 || value > maximumSafeCount {
		return "", fmt.Errorf("mutationCountOutOfRange")
	}
	return status, nil
}

func knownStatusName(name string) bool {
	_, err := normalizeStatus(name)
	return err == nil
}

func sumCounts(counts map[Status]int64) int64 {
	total := int64(0)
	for _, status := range statuses {
		value := counts[status]
		if value > maximumSafeCount-total {
			return -1
		}
		total += value
	}
	return total
}

func emptyCounts() map[Status]int64 {
	counts := make(map[Status]int64, len(statuses))
	for _, status := range statuses {
		counts[status] = 0
	}
	return counts
}

func countRecord(record MutantRecord, seen map[string]struct{}, counts map[string]int64) error {
	if record.CandidateID == "" {
		return fmt.Errorf("mutantCandidateIdInvalid")
	}
	if _, duplicate := seen[record.CandidateID]; duplicate {
		return fmt.Errorf("mutantCandidateIdDuplicate")
	}
	status, err := normalizeStatus(string(record.Status))
	if err != nil {
		return err
	}
	seen[record.CandidateID] = struct{}{}
	counts[string(status)]++
	return nil
}

func exactKillRate(killed, total int64) (*ExactRate, error) {
	numerator := big.NewInt(killed)
	denominator := big.NewInt(total)
	gcd := new(big.Int).GCD(nil, nil, numerator, denominator)
	numerator.Quo(numerator, gcd)
	denominator.Quo(denominator, gcd)
	decimal, err := crap.RenderCanonicalDecimal(numerator.String(), denominator.String())
	if err != nil {
		return nil, err
	}
	return &ExactRate{Numerator: numerator.String(), Denominator: denominator.String(), Decimal: decimal}, nil
}
