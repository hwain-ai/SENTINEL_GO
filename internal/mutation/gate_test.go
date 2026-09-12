package mutation

import (
	"reflect"
	"testing"
)

func TestNormalizeRequiresExactCandidateOutcomeSet(t *testing.T) {
	report := BridgeReport{
		SchemaVersion:   "sentinel-mutate4go-report-v1",
		BackendName:     "mutate4go",
		BackendCommit:   Mutate4GoCommit,
		SourceInventory: []SourceInventory{{Path: "value.go", SHA256: testDigest, CandidateCount: 2}},
		Candidates: []Candidate{
			{ID: "a", SourceFile: "value.go", Line: 3, Column: 10, Operator: "> -> >="},
			{ID: "b", SourceFile: "value.go", Line: 3, Column: 12, Operator: "0 -> 1"},
		},
		Outcomes: []RawOutcome{{CandidateID: "a", Status: "killed"}},
	}

	if _, err := Normalize(report); err == nil {
		t.Fatal("Normalize accepted a missing candidate outcome")
	}
}

func TestNormalizeRejectsDuplicateAndUnknownResults(t *testing.T) {
	base := BridgeReport{
		SchemaVersion:   "sentinel-mutate4go-report-v1",
		BackendName:     "mutate4go",
		BackendCommit:   Mutate4GoCommit,
		SourceInventory: []SourceInventory{{Path: "value.go", SHA256: testDigest, CandidateCount: 1}},
		Candidates:      []Candidate{{ID: "a", SourceFile: "value.go", Line: 3, Column: 10, Operator: "> -> >="}},
	}

	duplicate := base
	duplicate.Outcomes = []RawOutcome{{CandidateID: "a", Status: "killed"}, {CandidateID: "a", Status: "killed"}}
	if _, err := Normalize(duplicate); err == nil {
		t.Fatal("Normalize accepted a duplicate outcome")
	}

	unknown := base
	unknown.Outcomes = []RawOutcome{{CandidateID: "a", Status: "magic"}}
	if _, err := Normalize(unknown); err == nil {
		t.Fatal("Normalize accepted an unknown raw status")
	}
}

const testDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestNormalizeRequiresCompleteSourceInventory(t *testing.T) {
	report := BridgeReport{
		SchemaVersion: "sentinel-mutate4go-report-v1",
		BackendName:   "mutate4go",
		BackendCommit: Mutate4GoCommit,
		Candidates:    []Candidate{{ID: "a", SourceFile: "value.go", Line: 3, Column: 10, Operator: "> -> >="}},
		Outcomes:      []RawOutcome{{CandidateID: "a", Status: "killed"}},
	}
	if _, err := Normalize(report); err == nil {
		t.Fatal("Normalize accepted a report without source inventory")
	}
}

func TestStrictGateOnlyPassesOneOrMoreKilledMutants(t *testing.T) {
	records := []MutantRecord{{CandidateID: "a", Status: Killed}, {CandidateID: "b", Status: Killed}}
	before := append([]MutantRecord(nil), records...)

	result, err := Evaluate(records, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pass || result.InScope != 2 || result.KillRate == nil {
		t.Fatalf("result = %#v", result)
	}
	if result.KillRate.Numerator != "1" || result.KillRate.Denominator != "1" || result.KillRate.Decimal != "1" {
		t.Fatalf("kill rate = %#v", result.KillRate)
	}
	if !reflect.DeepEqual(records, before) {
		t.Fatal("Evaluate modified its input")
	}

	for _, status := range []Status{Survived, Uncovered, TimedOut, CompileError, RuntimeError, Pending, Ignored, ToolError} {
		failed, gateErr := Evaluate([]MutantRecord{{CandidateID: "a", Status: status}}, 0)
		if gateErr != nil {
			t.Fatalf("%s: %v", status, gateErr)
		}
		if failed.Pass {
			t.Fatalf("status %s passed", status)
		}
	}
}

func TestStrictGateRejectsZeroMutantsAndUnauthorizedExclusion(t *testing.T) {
	zero, err := Evaluate(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if zero.Pass || zero.KillRate != nil {
		t.Fatalf("zero-mutant result = %#v", zero)
	}

	excluded, err := Evaluate([]MutantRecord{{CandidateID: "a", Status: Killed}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if excluded.Pass {
		t.Fatal("unauthorized exclusion passed")
	}
}
