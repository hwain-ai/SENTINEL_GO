package crap

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

const (
	decimalPlaces = 12
	crapLimit     = 8
)

var decimalScale = new(big.Int).Exp(big.NewInt(10), big.NewInt(decimalPlaces), nil)

// Calculate evaluates CC^2 * (1 - coverage)^3 + CC using integer arithmetic.
func Calculate(complexity, covered, total int64) (ExactCrap, error) {
	if err := validateFormulaInputs(complexity, covered, total); err != nil {
		return ExactCrap{}, err
	}
	cc := big.NewInt(complexity)
	totalUnits := big.NewInt(total)
	uncovered := new(big.Int).Sub(totalUnits, big.NewInt(covered))
	denominator := new(big.Int).Exp(totalUnits, big.NewInt(3), nil)
	uncoveredCubed := new(big.Int).Exp(uncovered, big.NewInt(3), nil)
	ccSquared := new(big.Int).Mul(cc, cc)
	numerator := new(big.Int).Mul(ccSquared, uncoveredCubed)
	numerator.Add(numerator, new(big.Int).Mul(cc, denominator))

	numerator, denominator = reduceFraction(numerator, denominator)
	decimal, err := renderFraction(numerator, denominator)
	if err != nil {
		return ExactCrap{}, err
	}
	limit := new(big.Int).Mul(big.NewInt(crapLimit), denominator)
	return ExactCrap{
		Numerator:   numerator.String(),
		Denominator: denominator.String(),
		Decimal:     decimal,
		Pass:        numerator.Cmp(limit) <= 0,
	}, nil
}

func validateFormulaInputs(complexity, covered, total int64) error {
	if complexity < 1 || complexity > maximumJSONSafeInteger {
		return errors.New("cyclomaticComplexityOutOfRange")
	}
	if covered < 0 || covered > maximumJSONSafeInteger {
		return errors.New("coveredUnitsOutOfRange")
	}
	if total < 1 || total > maximumJSONSafeInteger {
		return errors.New("totalUnitsOutOfRange")
	}
	if covered > total {
		return errors.New("coveredUnitsExceedTotalUnits")
	}
	return nil
}

// RenderCanonicalDecimal renders a nonnegative fraction with half-even
// rounding at twelve decimal places and no insignificant trailing zeroes.
func RenderCanonicalDecimal(numeratorText, denominatorText string) (string, error) {
	numerator, ok := new(big.Int).SetString(numeratorText, 10)
	if !ok {
		return "", fmt.Errorf("invalid numerator %q", numeratorText)
	}
	denominator, ok := new(big.Int).SetString(denominatorText, 10)
	if !ok {
		return "", fmt.Errorf("invalid denominator %q", denominatorText)
	}
	return renderFraction(numerator, denominator)
}

func renderFraction(numerator, denominator *big.Int) (string, error) {
	if numerator.Sign() < 0 {
		return "", fmt.Errorf("numerator must be nonnegative")
	}
	if denominator.Sign() <= 0 {
		return "", fmt.Errorf("denominator must be positive")
	}

	scaled := new(big.Int).Mul(new(big.Int).Set(numerator), decimalScale)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(scaled, denominator, remainder)
	doubledRemainder := new(big.Int).Lsh(new(big.Int).Set(remainder), 1)
	comparison := doubledRemainder.Cmp(denominator)
	if comparison > 0 || (comparison == 0 && quotient.Bit(0) == 1) {
		quotient.Add(quotient, big.NewInt(1))
	}

	integerPart, fractionalPart := new(big.Int), new(big.Int)
	integerPart.QuoRem(quotient, decimalScale, fractionalPart)
	if fractionalPart.Sign() == 0 {
		return integerPart.String(), nil
	}
	fractionalText := fractionalPart.String()
	if missing := decimalPlaces - len(fractionalText); missing > 0 {
		fractionalText = strings.Repeat("0", missing) + fractionalText
	}
	fractionalText = strings.TrimRight(fractionalText, "0")
	return integerPart.String() + "." + fractionalText, nil
}

func reduceFraction(numerator, denominator *big.Int) (*big.Int, *big.Int) {
	gcd := new(big.Int).GCD(nil, nil, numerator, denominator)
	return new(big.Int).Quo(numerator, gcd), new(big.Int).Quo(denominator, gcd)
}
