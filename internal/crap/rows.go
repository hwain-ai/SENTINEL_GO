package crap

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"unicode/utf8"
)

// CrapRow contains the fields needed by the cross-language CRAP row order.
type CrapRow struct {
	ModuleRelativePath string `json:"moduleRelativePath"`
	SourceStartByte    int64  `json:"sourceStartByte"`
	CallableID         string `json:"callableId"`
	Numerator          string `json:"numerator,omitempty"`
	Denominator        string `json:"denominator,omitempty"`
	UnknownReason      string `json:"unknownReason,omitempty"`
}

type validatedCrapRow struct {
	row         CrapRow
	numerator   *big.Int
	denominator *big.Int
	unknown     bool
}

type crapRowIdentity struct {
	path       string
	startByte  int64
	callableID string
}

// SortRows validates rows and returns a new slice in the canonical SPEC order.
func SortRows(rows []CrapRow) ([]CrapRow, error) {
	validated, err := validateCrapRows(rows)
	if err != nil {
		return nil, err
	}
	sort.Slice(validated, func(left, right int) bool {
		return compareValidatedRows(validated[left], validated[right]) < 0
	})
	result := make([]CrapRow, len(validated))
	for index := range validated {
		result[index] = validated[index].row
	}
	return result, nil
}

func validateCrapRows(rows []CrapRow) ([]validatedCrapRow, error) {
	validated := make([]validatedCrapRow, 0, len(rows))
	seen := make(map[crapRowIdentity]struct{}, len(rows))
	for _, row := range rows {
		prepared, err := validateCrapRow(row)
		if err != nil {
			return nil, err
		}
		identity := crapRowIdentity{path: row.ModuleRelativePath, startByte: row.SourceStartByte, callableID: row.CallableID}
		if _, duplicate := seen[identity]; duplicate {
			return nil, fmt.Errorf("identityAmbiguous: duplicate CRAP row identity")
		}
		seen[identity] = struct{}{}
		validated = append(validated, prepared)
	}
	return validated, nil
}

func validateCrapRow(row CrapRow) (validatedCrapRow, error) {
	if err := validateModulePath(row.ModuleRelativePath); err != nil {
		return validatedCrapRow{}, fmt.Errorf("moduleRelativePathInvalid: %w", err)
	}
	if !validSourceStartByte(row.SourceStartByte) {
		return validatedCrapRow{}, fmt.Errorf("sourceStartByteInvalid: %d", row.SourceStartByte)
	}
	if !validCallableID(row.CallableID) {
		return validatedCrapRow{}, fmt.Errorf("callableIdInvalid")
	}
	if row.UnknownReason != "" {
		return validateUnknownCrapRow(row)
	}
	return validateKnownCrapRow(row)
}

func validSourceStartByte(sourceStartByte int64) bool {
	return sourceStartByte >= 0 && sourceStartByte <= maximumJSONSafeInteger
}

func validCallableID(callableID string) bool {
	return callableID != "" && utf8.ValidString(callableID) && !strings.ContainsRune(callableID, '\x00')
}

func validateUnknownCrapRow(row CrapRow) (validatedCrapRow, error) {
	if !utf8.ValidString(row.UnknownReason) || strings.ContainsRune(row.UnknownReason, '\x00') {
		return validatedCrapRow{}, fmt.Errorf("unknownReasonInvalid")
	}
	if row.Numerator != "" || row.Denominator != "" {
		return validatedCrapRow{}, fmt.Errorf("unknownCrapFractionPresent")
	}
	return validatedCrapRow{row: row, unknown: true}, nil
}

func validateKnownCrapRow(row CrapRow) (validatedCrapRow, error) {
	numerator, err := parseCanonicalUnsignedInteger(row.Numerator)
	if err != nil {
		return validatedCrapRow{}, fmt.Errorf("crapFractionInvalid: numerator: %w", err)
	}
	denominator, err := parseCanonicalUnsignedInteger(row.Denominator)
	if err != nil || denominator.Sign() == 0 {
		return validatedCrapRow{}, fmt.Errorf("crapFractionInvalid: denominator")
	}
	if !fractionIsReduced(numerator, denominator) {
		return validatedCrapRow{}, fmt.Errorf("crapFractionInvalid: fraction is not reduced")
	}
	return validatedCrapRow{row: row, numerator: numerator, denominator: denominator}, nil
}

func parseCanonicalUnsignedInteger(text string) (*big.Int, error) {
	value, ok := new(big.Int).SetString(text, 10)
	if !ok || value.Sign() < 0 || value.String() != text {
		return nil, fmt.Errorf("not a canonical unsigned integer")
	}
	return value, nil
}

func fractionIsReduced(numerator, denominator *big.Int) bool {
	gcd := new(big.Int).GCD(nil, nil, numerator, denominator)
	return gcd.Cmp(big.NewInt(1)) == 0
}

func compareValidatedRows(left, right validatedCrapRow) int {
	if left.unknown != right.unknown {
		if left.unknown {
			return -1
		}
		return 1
	}
	if !left.unknown {
		if riskOrder := compareExactRiskDescending(left, right); riskOrder != 0 {
			return riskOrder
		}
	}
	return compareCrapRowIdentity(left.row, right.row)
}

func compareExactRiskDescending(left, right validatedCrapRow) int {
	leftCrossProduct := new(big.Int).Mul(left.numerator, right.denominator)
	rightCrossProduct := new(big.Int).Mul(right.numerator, left.denominator)
	return -leftCrossProduct.Cmp(rightCrossProduct)
}

func compareCrapRowIdentity(left, right CrapRow) int {
	if pathOrder := strings.Compare(left.ModuleRelativePath, right.ModuleRelativePath); pathOrder != 0 {
		return pathOrder
	}
	if left.SourceStartByte < right.SourceStartByte {
		return -1
	}
	if left.SourceStartByte > right.SourceStartByte {
		return 1
	}
	return strings.Compare(left.CallableID, right.CallableID)
}
