package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreKeepsImmutableRunsAndFindsRepeatedDefects(t *testing.T) {
	project := t.TempDir()
	store := NewStore(project)
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := store.Fingerprint("mutation", "survived", "stable-candidate")
	if err != nil {
		t.Fatal(err)
	}

	first := validHistoryRun("00000000-0000-4000-8000-000000000001", "mutation", "qualityFailed", time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC))
	first.Findings = []Finding{{Fingerprint: fingerprint, Component: "mutation", Kind: "survived"}}
	second := validHistoryRun("00000000-0000-4000-8000-000000000002", "mutation", "qualityFailed", first.OccurredAtUTC.Add(time.Hour))
	second.Findings = append([]Finding(nil), first.Findings...)

	if err := store.Start(first.RunID, first.Command, first.OccurredAtUTC); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(first); err != nil {
		t.Fatal(err)
	}
	if err := store.Start(second.RunID, second.Command, second.OccurredAtUTC); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(second); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(second); err == nil {
		t.Fatal("duplicate run ID overwrote immutable history")
	}

	runs, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].RunID != first.RunID || runs[1].RunID != second.RunID {
		t.Fatalf("runs = %#v", runs)
	}
	repeated := RepeatedDefects(runs)
	if len(repeated) != 1 || repeated[0].Fingerprint != fingerprint || repeated[0].ObservationCount != 2 || !repeated[0].Repeated {
		t.Fatalf("repeated = %#v", repeated)
	}
	if filepath.Base(store.Root()) != "state-v1" {
		t.Fatalf("history root = %q", store.Root())
	}
}

func TestLoadRejectsTrailingJSONInsteadOfIgnoringCorruption(t *testing.T) {
	project := t.TempDir()
	store := NewStore(project)
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	run := validHistoryRun("00000000-0000-4000-8000-000000000004", "crap", "passed", time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC))
	if err := store.Start(run.RunID, run.Command, run.OccurredAtUTC); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(run); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root(), "runs", run.RunID, "evidence.json")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("history accepted trailing JSON")
	}
}

func TestHistoryRejectsRawIdentityInFinding(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := store.Fingerprint("crap", "aboveLimit", "callable")
	if err != nil {
		t.Fatal(err)
	}
	run := validHistoryRun("00000000-0000-4000-8000-000000000003", "crap", "qualityFailed", time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC))
	run.Findings = []Finding{{
		Fingerprint: fingerprint,
		Component:   "crap",
		Kind:        "aboveLimit",
		RawIdentity: "internal/private/file.go:Secret",
	}}
	if err := store.Start(run.RunID, run.Command, run.OccurredAtUTC); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(run); err == nil {
		t.Fatal("history accepted a raw identity")
	}
}

func TestRepeatedHistoryUsesCommitOrderWhenUTCMovesBackward(t *testing.T) {
	fingerprint := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	laterUTC := time.Date(2026, 9, 3, 2, 0, 0, 0, time.UTC)
	earlierUTC := laterUTC.Add(-time.Hour)
	runs := []RunRecord{
		{RunID: "00000000-0000-4000-8000-000000000001", CommitSequence: "1", OccurredAtUTC: laterUTC, Findings: []Finding{{Fingerprint: fingerprint, Component: "crap", Kind: "aboveLimit"}}},
		{RunID: "00000000-0000-4000-8000-000000000002", CommitSequence: "2", OccurredAtUTC: earlierUTC, Findings: []Finding{{Fingerprint: fingerprint, Component: "crap", Kind: "aboveLimit"}}},
	}

	defects := RepeatedDefects(runs)
	if len(defects) != 1 || !defects[0].FirstSeenUTC.Equal(laterUTC) || !defects[0].LastSeenUTC.Equal(earlierUTC) {
		t.Fatalf("defects = %#v", defects)
	}
}

func validHistoryRun(runID, command, terminalStatus string, startedAt time.Time) RunRecord {
	record := RunRecord{
		Command:            command,
		CommittedAtUTC:     startedAt.Add(2 * time.Second),
		CompletedAtUTC:     startedAt.Add(time.Second),
		CorrelationID:      runID,
		DiagnosticCodes:    []string{},
		FingerprintVersion: "sentinel-fingerprint-v1",
		Language:           "go",
		Mode:               "strict",
		ObservationSource:  "fresh",
		OccurredAtUTC:      startedAt,
		RunID:              runID,
		SchemaVersion:      RunSchemaVersion,
		SpecVersion:        "1.0.0",
		TerminalStatus:     terminalStatus,
	}
	if command == "mutation" {
		record.Components.Mutation = &MutationComponent{InScope: 1, Survived: 1}
		record.ExitCode = 2
		return record
	}
	if terminalStatus == "passed" {
		record.Certification = true
		record.Components.Crap = &CrapComponent{CallableCount: 1, MaxNumerator: "1", MaxDenominator: "1", Pass: true}
		return record
	}
	record.Components.Crap = &CrapComponent{CallableCount: 1, MaxNumerator: "9", MaxDenominator: "1"}
	record.ExitCode = 2
	return record
}
