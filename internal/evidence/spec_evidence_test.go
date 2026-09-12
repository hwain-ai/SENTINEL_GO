package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const evidenceGoldenSHA256 = "e1f18d2b678445a762a5f6231c408012b247ccc7b007c530b1a4261c427a1114"

type evidenceGolden struct {
	CleanupLeaseKeyHex  string `json:"cleanupLeaseKeyHex"`
	ProjectStateFileHex string `json:"projectStateFileHex"`
	ValidCases          []struct {
		ID            string          `json:"id"`
		Body          json.RawMessage `json:"body"`
		BodySHA256    string          `json:"bodySha256"`
		DerivedKeyHex string          `json:"derivedKeyHex"`
		ExpectedHMAC  string          `json:"expectedHmac"`
		FileSHA256    string          `json:"fileSha256"`
	} `json:"validCases"`
	SemanticInvalidCases []struct {
		ID       string          `json:"id"`
		BaseCase string          `json:"baseCase"`
		Field    string          `json:"field"`
		Value    json.RawMessage `json:"value"`
		Error    string          `json:"error"`
	} `json:"semanticInvalidCases"`
	WireInvalidCases []struct {
		ID       string          `json:"id"`
		Document json.RawMessage `json:"document"`
	} `json:"wireInvalidCases"`
}

func TestSPECGoldenEvidenceVectorsHaveExactBytesAndHMAC(t *testing.T) {
	vectors, keys := loadEvidenceGolden(t)
	defer keys.clear()
	for _, testCase := range vectors.ValidCases {
		t.Run(testCase.ID, func(t *testing.T) {
			body := decodeGoldenEvidenceBody(t, testCase.Body)
			if err := validateEvidenceBody(body, keys, true); err != nil {
				t.Fatalf("golden body validation: %v", err)
			}
			bodyPayload, err := canonicalBody(body)
			if err != nil {
				t.Fatal(err)
			}
			if got := sha256Hex(bodyPayload); got != testCase.BodySHA256 {
				t.Fatalf("body SHA-256 = %s, want %s", got, testCase.BodySHA256)
			}
			derived := deriveHMACKey(keys.cleanupLeaseKey, evidenceKeyNamespace)
			if got := hex.EncodeToString(derived); got != testCase.DerivedKeyHex {
				clear(derived)
				t.Fatalf("derived key = %s, want %s", got, testCase.DerivedKeyHex)
			}
			mac := namespacedHMAC(derived, evidenceMacNamespace, bodyPayload)
			clear(derived)
			if mac != testCase.ExpectedHMAC {
				t.Fatalf("evidence HMAC = %s, want %s", mac, testCase.ExpectedHMAC)
			}
			payload, err := canonicalJSON(evidenceRecordFromBody(body, mac))
			if err != nil {
				t.Fatal(err)
			}
			if got := sha256Hex(payload); got != testCase.FileSHA256 {
				t.Fatalf("file SHA-256 = %s, want %s", got, testCase.FileSHA256)
			}
		})
	}
}

func TestSPECGoldenSemanticCounterexamplesFailClosed(t *testing.T) {
	vectors, keys := loadEvidenceGolden(t)
	defer keys.clear()
	valid := make(map[string]json.RawMessage, len(vectors.ValidCases))
	for _, testCase := range vectors.ValidCases {
		valid[testCase.ID] = testCase.Body
	}
	for _, testCase := range vectors.SemanticInvalidCases {
		t.Run(testCase.ID, func(t *testing.T) {
			document := decodeJSONValue(t, valid[testCase.BaseCase]).(map[string]any)
			setJSONPath(t, document, testCase.Field, decodeJSONValue(t, testCase.Value))
			payload, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			body := decodeGoldenEvidenceBody(t, payload)
			err = validateEvidenceBody(body, keys, false)
			if err == nil || !strings.Contains(err.Error(), testCase.Error) {
				t.Fatalf("error = %v, want %s", err, testCase.Error)
			}
		})
	}
}

func TestLegacyGoEvidenceEnvelopeIsRejectedByExactDecoder(t *testing.T) {
	vectors, _ := loadEvidenceGolden(t)
	for _, testCase := range vectors.WireInvalidCases {
		if testCase.ID != "go-legacy-envelope" {
			continue
		}
		var document any
		if err := json.Unmarshal(testCase.Document, &document); err != nil {
			t.Fatal(err)
		}
		payload, err := canonicalJSON(document)
		if err != nil {
			t.Fatal(err)
		}
		var record evidenceRecord
		if err := decodeCanonical(payload, &record); err == nil {
			t.Fatal("legacy 11-field Go evidence envelope was accepted")
		}
		return
	}
	t.Fatal("go-legacy-envelope fixture is missing")
}

func TestEvidenceManifestRejectsEveryClosedShapeBoundary(t *testing.T) {
	vectors, keys := loadEvidenceGolden(t)
	defer keys.clear()
	base := decodeGoldenEvidenceBody(t, vectors.ValidCases[0].Body)
	badDigest := base
	badDigest.Events = append([]eventManifestEntry(nil), base.Events...)
	badDigest.Events[0].SHA256 = "A" + badDigest.Events[0].SHA256[1:]
	badFilename := base
	badFilename.Events = append([]eventManifestEntry(nil), base.Events...)
	badFilename.Events[0].Filename = "../event.json"
	duplicate := base
	duplicate.EventCount = 2
	duplicate.Events = []eventManifestEntry{base.Events[0], base.Events[0]}
	wrongOrdinal := base
	wrongOrdinal.Events = append([]eventManifestEntry(nil), base.Events...)
	wrongOrdinal.Events[0].Filename = "00000000000000000000000000000002.json"
	cacheWithEvent := base
	cacheWithEvent.Certification = false
	cacheWithEvent.Mode = "local"
	cacheWithEvent.ObservationSource = "cache"
	source := contractTestRunID3
	cacheWithEvent.SourceRunID = &source

	tests := []struct {
		name string
		body evidenceBody
		code string
	}{
		{"unsafe count", func() evidenceBody { value := base; value.EventCount = maxSafeInteger + 1; return value }(), "eventManifestInvalid"},
		{"nil events", func() evidenceBody { value := base; value.Events = nil; return value }(), "eventManifestInvalid"},
		{"count mismatch", func() evidenceBody { value := base; value.EventCount = 2; return value }(), "eventManifestCountMismatch"},
		{"bad filename", badFilename, "eventManifestFilenameInvalid"},
		{"bad digest", badDigest, "eventManifestDigestInvalid"},
		{"duplicate", duplicate, "eventManifestDuplicate"},
		{"wrong ordinal", wrongOrdinal, "eventManifestOrdinalInvalid"},
		{"cache event", cacheWithEvent, "cacheObservationHasEvents"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateEvidenceBody(testCase.body, keys, false)
			if err == nil || !strings.Contains(err.Error(), testCase.code) {
				t.Fatalf("error = %v, want %s", err, testCase.code)
			}
		})
	}
}

func TestCanonicalEvidenceTimestampRejectsYearZeroLikeSPEC(t *testing.T) {
	if _, err := parseCanonicalUTC("0000-01-01T00:00:00Z"); err == nil {
		t.Fatal("year-zero timestamp was accepted")
	}
}

func TestStoreWritesCurrentEpochAndReadsAuthenticatedHistoryAfterRotation(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := store.Fingerprint("mutation", "survived", "candidate-one")
	if err != nil {
		t.Fatal(err)
	}
	first := testRun(contractTestRunID, fingerprint)
	if err := store.Start(first.RunID, first.Command, first.OccurredAtUTC); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(first); err != nil {
		t.Fatal(err)
	}
	firstRecord := readEvidenceRecordForTest(t, store, first.RunID)
	if firstRecord.KeyEpoch != 1 {
		t.Fatalf("first keyEpoch = %d", firstRecord.KeyEpoch)
	}

	projectPath := filepath.Join(store.Root(), "project.json")
	var state projectState
	readCanonicalTestFile(t, projectPath, &state)
	state.FingerprintHMACKey = "REREREREREREREREREREREREREREREREREREREREREQ"
	state.KeyEpoch = 2
	writeCanonicalTestFile(t, projectPath, state)
	rotatedKeys, err := readProjectKeys(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rotatedKeys.clear()

	if _, err := store.Load(); err != nil {
		t.Fatalf("authenticated epoch-1 history rejected at epoch 2: %v", err)
	}
	if err := validateEvidenceBody(evidenceBodyFromRecord(firstRecord), rotatedKeys, false); err == nil || !strings.Contains(err.Error(), "keyEpochInvalid") {
		t.Fatalf("new evidence accepted stale epoch: %v", err)
	}

	secondFingerprint, err := store.Fingerprint("mutation", "survived", "candidate-two")
	if err != nil {
		t.Fatal(err)
	}
	second := testRun(contractTestRunID3, secondFingerprint)
	if err := store.Start(second.RunID, second.Command, second.OccurredAtUTC); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(second); err != nil {
		t.Fatal(err)
	}
	if got := readEvidenceRecordForTest(t, store, second.RunID).KeyEpoch; got != 2 {
		t.Fatalf("new evidence keyEpoch = %d, want 2", got)
	}
}

func loadEvidenceGolden(t *testing.T) (evidenceGolden, projectKeys) {
	t.Helper()
	path := filepath.Join("testdata", "spec", "evidence-v1.json")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if got := hex.EncodeToString(digest[:]); got != evidenceGoldenSHA256 {
		t.Fatalf("evidence golden SHA-256 = %s", got)
	}
	var vectors evidenceGolden
	if err := json.Unmarshal(payload, &vectors); err != nil {
		t.Fatal(err)
	}
	projectPayload, err := hex.DecodeString(vectors.ProjectStateFileHex)
	if err != nil {
		t.Fatal(err)
	}
	var state projectState
	if err := decodeCanonical(projectPayload, &state); err != nil {
		t.Fatal(err)
	}
	keys, err := decodeProjectKeys(state)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(keys.cleanupLeaseKey); got != vectors.CleanupLeaseKeyHex {
		keys.clear()
		t.Fatalf("cleanup key = %s", got)
	}
	return vectors, keys
}

func decodeGoldenEvidenceBody(t *testing.T, payload []byte) evidenceBody {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var body evidenceBody
	if err := decoder.Decode(&body); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("golden body trailing data: %v", err)
	}
	return body
}

func decodeJSONValue(t *testing.T, payload []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func setJSONPath(t *testing.T, document map[string]any, path string, value any) {
	t.Helper()
	parts := strings.Split(path, ".")
	target := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := target[part].(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object in %s", part, path)
		}
		target = next
	}
	target[parts[len(parts)-1]] = value
}

func readEvidenceRecordForTest(t *testing.T, store *Store, runID string) evidenceRecord {
	t.Helper()
	var record evidenceRecord
	readCanonicalTestFile(t, filepath.Join(store.Root(), "runs", runID, "evidence.json"), &record)
	return record
}

func TestGoldenFixtureIncludesAllExpectedCases(t *testing.T) {
	vectors, keys := loadEvidenceGolden(t)
	defer keys.clear()
	if len(vectors.ValidCases) != 3 || len(vectors.SemanticInvalidCases) < 16 {
		t.Fatalf("unexpected golden coverage: valid=%d invalid=%d", len(vectors.ValidCases), len(vectors.SemanticInvalidCases))
	}
	if projectStateHMAC(keys) != "441395de4352207dc696516a31efa8fb34fc5d4df9a9005537342cda21e84354" {
		t.Fatal(fmt.Errorf("project state binding does not match SPEC"))
	}
}
