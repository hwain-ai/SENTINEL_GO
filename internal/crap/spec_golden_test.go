package crap

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"reflect"
	"strings"
	"testing"
)

const (
	formulaGoldenDigest = "c49493dc25c841af08efd6b1984fdeae47b54a5345e71dea137de96c45fe8884"
	rowGoldenDigest     = "921f87cdb842042fad914147c2d4daded2654175088c9284abc3cff0cbff1f06"
)

type formulaGolden struct {
	SchemaVersion string `json:"schemaVersion"`
	FormulaCases  []struct {
		ID    string `json:"id"`
		Input struct {
			Complexity int64 `json:"cyclomaticComplexity"`
			Covered    int64 `json:"coveredUnits"`
			Total      int64 `json:"totalUnits"`
		} `json:"input"`
		Expected struct {
			Numerator   string `json:"numerator"`
			Denominator string `json:"denominator"`
			Decimal     string `json:"decimal"`
			Pass        bool   `json:"pass"`
		} `json:"expected"`
	} `json:"formulaCases"`
	DecimalCases []struct {
		ID          string `json:"id"`
		Numerator   string `json:"numerator"`
		Denominator string `json:"denominator"`
		Expected    string `json:"expected"`
	} `json:"decimalCases"`
	InvalidCases []struct {
		ID    string          `json:"id"`
		Input json.RawMessage `json:"input"`
		Error string          `json:"error"`
	} `json:"invalidCases"`
}

type formulaInput struct {
	Complexity int64 `json:"cyclomaticComplexity"`
	Covered    int64 `json:"coveredUnits"`
	Total      int64 `json:"totalUnits"`
}

type rowOrderGolden struct {
	SchemaVersion string `json:"schemaVersion"`
	Cases         []struct {
		ID   string    `json:"id"`
		Rows []CrapRow `json:"rows"`
		Want []string  `json:"expectedCallableIds"`
	} `json:"cases"`
	InvalidCases []struct {
		ID            string    `json:"id"`
		Rows          []CrapRow `json:"rows"`
		ExpectedError string    `json:"expectedError"`
	} `json:"invalidCases"`
}

func TestCurrentSpecFormulaGolden(t *testing.T) {
	payload := readPinnedGolden(t, "testdata/spec/formula-v1.json", formulaGoldenDigest)
	var golden formulaGolden
	if err := json.Unmarshal(payload, &golden); err != nil {
		t.Fatal(err)
	}
	if golden.SchemaVersion != "sentinel-crap-formula-v1" {
		t.Fatalf("schemaVersion = %q", golden.SchemaVersion)
	}
	for _, testCase := range golden.FormulaCases {
		got, err := Calculate(testCase.Input.Complexity, testCase.Input.Covered, testCase.Input.Total)
		if err != nil {
			t.Fatalf("%s: %v", testCase.ID, err)
		}
		want := testCase.Expected
		if got.Numerator != want.Numerator || got.Denominator != want.Denominator || got.Decimal != want.Decimal || got.Pass != want.Pass {
			t.Fatalf("%s: Calculate = %#v, want %#v", testCase.ID, got, want)
		}
	}
	for _, testCase := range golden.DecimalCases {
		got, err := RenderCanonicalDecimal(testCase.Numerator, testCase.Denominator)
		if err != nil {
			t.Fatalf("%s: %v", testCase.ID, err)
		}
		if got != testCase.Expected {
			t.Fatalf("%s: decimal = %q, want %q", testCase.ID, got, testCase.Expected)
		}
	}
	if len(golden.InvalidCases) == 0 {
		t.Fatal("formula golden has no invalid cases")
	}
	for _, testCase := range golden.InvalidCases {
		if testCase.Error == "" {
			t.Fatalf("%s: missing expected error code", testCase.ID)
		}
		if got := calculateInvalidGoldenInput(testCase.Input); got != testCase.Error {
			t.Fatalf("%s: error code = %q, want %q", testCase.ID, got, testCase.Error)
		}
	}
}

func calculateInvalidGoldenInput(payload json.RawMessage) string {
	var input formulaInput
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return formulaDecodeErrorCode(err)
	}
	_, err := Calculate(input.Complexity, input.Covered, input.Total)
	if err == nil {
		return ""
	}
	return err.Error()
}

func formulaDecodeErrorCode(err error) string {
	var typeError *json.UnmarshalTypeError
	if !errors.As(err, &typeError) {
		return "formulaInputInvalid"
	}
	switch typeError.Field {
	case "cyclomaticComplexity":
		return "cyclomaticComplexityNotInteger"
	case "coveredUnits":
		return "coveredUnitsNotInteger"
	case "totalUnits":
		return "totalUnitsNotInteger"
	default:
		return "formulaInputInvalid"
	}
}

func TestCalculateMatchesIndependentExactProperty(t *testing.T) {
	checked := 0
	for complexity := int64(1); complexity <= 50; complexity++ {
		for total := int64(1); total <= 50; total++ {
			for covered := int64(0); covered <= total; covered++ {
				got, err := Calculate(complexity, covered, total)
				if err != nil {
					t.Fatal(err)
				}
				want := independentCrapFraction(complexity, covered, total)
				if got.Numerator != want.Num().String() || got.Denominator != want.Denom().String() {
					t.Fatalf("Calculate(%d,%d,%d) = %s/%s, want %s", complexity, covered, total, got.Numerator, got.Denominator, want.RatString())
				}
				if got.Pass != (want.Cmp(big.NewRat(8, 1)) <= 0) {
					t.Fatalf("Calculate(%d,%d,%d).Pass = %t", complexity, covered, total, got.Pass)
				}
				checked++
			}
		}
	}
	if checked != 66250 {
		t.Fatalf("checked = %d, want 66250", checked)
	}
}

func TestCalculateRejectsJSONUnsafeCounts(t *testing.T) {
	unsafe := maximumJSONSafeInteger + 1
	cases := [][3]int64{
		{unsafe, 1, 1},
		{1, unsafe, unsafe},
		{1, 0, unsafe},
	}
	for _, testCase := range cases {
		if _, err := Calculate(testCase[0], testCase[1], testCase[2]); err == nil {
			t.Fatalf("Calculate(%d,%d,%d) accepted a JSON-unsafe integer", testCase[0], testCase[1], testCase[2])
		}
	}
}

func independentCrapFraction(complexity, covered, total int64) *big.Rat {
	cc := big.NewInt(complexity)
	totalUnits := big.NewInt(total)
	missed := big.NewInt(total - covered)
	denominator := new(big.Int).Exp(totalUnits, big.NewInt(3), nil)
	numerator := new(big.Int).Mul(new(big.Int).Mul(cc, cc), new(big.Int).Exp(missed, big.NewInt(3), nil))
	numerator.Add(numerator, new(big.Int).Mul(cc, denominator))
	return new(big.Rat).SetFrac(numerator, denominator)
}

func TestCurrentSpecStableRowOrderGolden(t *testing.T) {
	payload := readPinnedGolden(t, "testdata/spec/stable-sort-v1.json", rowGoldenDigest)
	var golden rowOrderGolden
	if err := json.Unmarshal(payload, &golden); err != nil {
		t.Fatal(err)
	}
	if golden.SchemaVersion != "sentinel-crap-row-order-v1" {
		t.Fatalf("schemaVersion = %q", golden.SchemaVersion)
	}
	for _, testCase := range golden.Cases {
		original := append([]CrapRow(nil), testCase.Rows...)
		sorted, err := SortRows(testCase.Rows)
		if err != nil {
			t.Fatalf("%s: %v", testCase.ID, err)
		}
		if !reflect.DeepEqual(testCase.Rows, original) {
			t.Fatalf("%s: SortRows changed its input", testCase.ID)
		}
		if got := callableIDs(sorted); !reflect.DeepEqual(got, testCase.Want) {
			t.Fatalf("%s: callable IDs = %#v, want %#v", testCase.ID, got, testCase.Want)
		}
	}
	for _, testCase := range golden.InvalidCases {
		_, err := SortRows(testCase.Rows)
		if err == nil || !strings.Contains(err.Error(), testCase.ExpectedError) {
			t.Fatalf("%s: error = %v, want %q", testCase.ID, err, testCase.ExpectedError)
		}
	}
}

func TestSortRowsUsesArbitraryPrecisionFractions(t *testing.T) {
	rows := []CrapRow{
		{ModuleRelativePath: "huge.go", SourceStartByte: 1, CallableID: "lower", Numerator: "100000000000000000000000000000000000000000000000001", Denominator: "1"},
		{ModuleRelativePath: "huge.go", SourceStartByte: 2, CallableID: "higher", Numerator: "100000000000000000000000000000000000000000000000002", Denominator: "1"},
	}
	sorted, err := SortRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	if sorted[0].CallableID != "higher" {
		t.Fatalf("first callable = %q, want higher", sorted[0].CallableID)
	}
}

func TestSortRowsRejectsNoncanonicalRows(t *testing.T) {
	cases := []CrapRow{
		{ModuleRelativePath: "./bad.go", SourceStartByte: 1, CallableID: "bad", Numerator: "1", Denominator: "1"},
		{ModuleRelativePath: "bad.go", SourceStartByte: -1, CallableID: "bad", Numerator: "1", Denominator: "1"},
		{ModuleRelativePath: "bad.go", SourceStartByte: 1, CallableID: "", Numerator: "1", Denominator: "1"},
		{ModuleRelativePath: "bad.go", SourceStartByte: 1, CallableID: "bad", Numerator: "01", Denominator: "1"},
		{ModuleRelativePath: "bad.go", SourceStartByte: 1, CallableID: "bad", Numerator: "1", Denominator: "0"},
		{ModuleRelativePath: "bad.go", SourceStartByte: 1, CallableID: "bad", Numerator: "2", Denominator: "2"},
		{ModuleRelativePath: "bad.go", SourceStartByte: 1, CallableID: "bad", Numerator: "1", Denominator: "1", UnknownReason: "coverageFileMissing"},
	}
	for _, row := range cases {
		if _, err := SortRows([]CrapRow{row}); err == nil {
			t.Fatalf("SortRows accepted noncanonical row %#v", row)
		}
	}
}

func readPinnedGolden(t *testing.T, path, wantDigest string) []byte {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if got := hex.EncodeToString(digest[:]); got != wantDigest {
		t.Fatalf("%s digest = %s, want %s", path, got, wantDigest)
	}
	return payload
}

func callableIDs(rows []CrapRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.CallableID)
	}
	return ids
}
