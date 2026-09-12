package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

const (
	contractTestRunID  = "00000000-0000-4000-8000-000000000001"
	contractTestRunID3 = "00000000-0000-4000-8000-000000000003"
)

func TestEvidenceUsesSharedSchemaAndUUIDContract(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}

	var project map[string]any
	readContractJSON(t, filepath.Join(store.Root(), "project.json"), &project)
	if project["schemaVersion"] != "sentinel-project-state-v1" || project["stateVersion"] != "state-v1" {
		t.Fatalf("project contract = %#v", project)
	}

	run := testRun(contractTestRunID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := store.Start(run.RunID, run.Command, run.OccurredAtUTC); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(run); err != nil {
		t.Fatal(err)
	}

	var marker map[string]any
	readContractJSON(t, filepath.Join(store.Root(), "runs", run.RunID, "evidence.json"), &marker)
	if marker["schemaVersion"] != "sentinel-evidence-v1" {
		t.Fatalf("evidence schema = %#v", marker["schemaVersion"])
	}
	if marker["commitSequence"] != "1" {
		t.Fatalf("top-level commitSequence = %#v", marker["commitSequence"])
	}
	if mac, valid := marker["hmacSha256"].(string); !valid || !hexDigest.MatchString(mac) {
		t.Fatalf("hmacSha256 = %#v", marker["hmacSha256"])
	}
	wantFields := []string{
		"certification", "command", "commitSequence", "committedAtUtc", "completedAtUtc",
		"components", "correlationId", "diagnosticCodes", "eventCount", "events", "exitCode",
		"fingerprintVersion", "hmacSha256", "keyEpoch", "language", "mode", "observationSource",
		"projectStateHmac", "runId", "schemaVersion", "sourceRunId", "specVersion", "startedAtUtc",
		"startedSha256", "terminalStatus",
	}
	gotFields := make([]string, 0, len(marker))
	for name := range marker {
		gotFields = append(gotFields, name)
	}
	sort.Strings(gotFields)
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("evidence fields = %#v", gotFields)
	}
	if _, legacy := marker["fingerprintEpoch"]; legacy {
		t.Fatal("legacy fingerprintEpoch leaked into evidence")
	}
}

func TestStartAcceptsCanonicalUUIDAndRejectsNonCanonicalRunID(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	if err := store.Start("00000000-0000-1000-7000-000000000001", "crap", testRun(contractTestRunID, "a").OccurredAtUTC); err != nil {
		t.Fatalf("canonical non-v4 UUID rejected: %v", err)
	}
	for _, runID := range []string{
		"00000000000040008000000000000001",
		"00000000-0000-4000-8000-00000000000A",
		"00000000-0000-4000-8000-00000000001",
	} {
		if err := store.Start(runID, "crap", testRun(contractTestRunID, "a").OccurredAtUTC); err == nil {
			t.Fatalf("invalid runId accepted: %q", runID)
		}
	}
}

func readContractJSON(t *testing.T, path string, target any) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		t.Fatal(err)
	}
}
