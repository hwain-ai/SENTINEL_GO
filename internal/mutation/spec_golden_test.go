package mutation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const mutationGoldenDigest = "98586e975cb68731c34402a2e5d98d168e2ae7471117201c9ffd1cab7b3c5cc0"

type mutationGolden struct {
	SchemaVersion string `json:"schemaVersion"`
	Cases         []struct {
		ID                    string           `json:"id"`
		Counts                map[string]int64 `json:"counts"`
		InScope               int64            `json:"inScope"`
		UnauthorizedExclusion int64            `json:"unauthorizedExclusion"`
		Expected              struct {
			Pass     bool       `json:"pass"`
			KillRate *ExactRate `json:"killRate"`
		} `json:"expected"`
	} `json:"cases"`
	InvalidCases []struct {
		ID                    string                     `json:"id"`
		Counts                map[string]json.RawMessage `json:"counts"`
		InScope               json.RawMessage            `json:"inScope"`
		UnauthorizedExclusion json.RawMessage            `json:"unauthorizedExclusion"`
		Error                 string                     `json:"error"`
	} `json:"invalidCases"`
	RawJSONInvalidCases []struct {
		ID        string `json:"id"`
		RawInput  string `json:"rawInput"`
		JSONError string `json:"jsonError"`
	} `json:"rawJsonInvalidCases"`
}

func TestMutationGateMatchesPinnedSpecGolden(t *testing.T) {
	golden := readMutationGolden(t)
	for _, testCase := range golden.Cases {
		result, err := EvaluateCounts(testCase.Counts, testCase.InScope, testCase.UnauthorizedExclusion)
		if err != nil {
			t.Fatalf("%s: %v", testCase.ID, err)
		}
		if result.Pass != testCase.Expected.Pass || !sameGoldenRate(result.KillRate, testCase.Expected.KillRate) {
			t.Fatalf("%s: result=%#v expected=%#v", testCase.ID, result, testCase.Expected)
		}
	}
}

func sameGoldenRate(actual, expected *ExactRate) bool {
	if actual == nil || expected == nil {
		return actual == nil && expected == nil
	}
	return actual.Numerator == expected.Numerator && actual.Denominator == expected.Denominator && actual.Decimal != ""
}

func TestMutationGateRejectsPinnedSemanticInvalidCases(t *testing.T) {
	golden := readMutationGolden(t)
	for _, testCase := range golden.InvalidCases {
		payload := assembleGateInput(t, testCase.Counts, testCase.InScope, testCase.UnauthorizedExclusion)
		_, err := DecodeAndEvaluate(payload)
		if err == nil || err.Error() != testCase.Error {
			t.Fatalf("%s: error=%v want=%s", testCase.ID, err, testCase.Error)
		}
	}
}

func TestMutationGateRejectsUnsafeRawJSONBeforeIntegerConversion(t *testing.T) {
	golden := readMutationGolden(t)
	for _, testCase := range golden.RawJSONInvalidCases {
		_, err := DecodeAndEvaluate([]byte(testCase.RawInput))
		if err == nil || err.Error() != testCase.JSONError {
			t.Fatalf("%s: error=%v want=%s", testCase.ID, err, testCase.JSONError)
		}
	}
	for _, payload := range [][]byte{
		[]byte(`{"counts":{"killed":1,"killed":0,"survived":0,"uncovered":0,"timedOut":0,"compileError":0,"runtimeError":0,"pending":0,"ignored":0,"toolError":0},"inScope":1,"unauthorizedExclusion":0}`),
		[]byte(`{"counts":{"killed":1.0,"survived":0,"uncovered":0,"timedOut":0,"compileError":0,"runtimeError":0,"pending":0,"ignored":0,"toolError":0},"inScope":1,"unauthorizedExclusion":0}`),
	} {
		if _, err := DecodeAndEvaluate(payload); err == nil {
			t.Fatalf("unsafe JSON passed: %s", payload)
		}
	}
}

func readMutationGolden(t *testing.T) mutationGolden {
	t.Helper()
	payload, err := os.ReadFile("testdata/spec/mutation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if got := hex.EncodeToString(digest[:]); got != mutationGoldenDigest {
		t.Fatalf("golden digest=%s", got)
	}
	var golden mutationGolden
	if err := json.Unmarshal(payload, &golden); err != nil {
		t.Fatal(err)
	}
	return golden
}

func assembleGateInput(t *testing.T, counts map[string]json.RawMessage, inScope, exclusion json.RawMessage) []byte {
	t.Helper()
	countPairs := make([]string, 0, len(counts))
	for name, value := range counts {
		encodedName, err := json.Marshal(name)
		if err != nil {
			t.Fatal(err)
		}
		countPairs = append(countPairs, string(encodedName)+":"+string(value))
	}
	payload := `{"counts":{` + strings.Join(countPairs, ",") + `},"inScope":` + string(inScope) + `,"unauthorizedExclusion":` + string(exclusion) + `}`
	return []byte(payload)
}
