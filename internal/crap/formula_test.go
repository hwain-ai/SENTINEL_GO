package crap

import "testing"

func TestCalculateUsesExactIntegerArithmetic(t *testing.T) {
	value, err := Calculate(4, 3, 4)
	if err != nil {
		t.Fatalf("Calculate returned error: %v", err)
	}
	if value.Numerator != "17" || value.Denominator != "4" {
		t.Fatalf("fraction = %s/%s, want 17/4", value.Numerator, value.Denominator)
	}
	if value.Decimal != "4.25" || !value.Pass {
		t.Fatalf("value = %#v, want decimal 4.25 and pass", value)
	}
}

func TestCalculateUsesExactEightBoundary(t *testing.T) {
	atBoundary, err := Calculate(8, 1, 1)
	if err != nil {
		t.Fatalf("Calculate at boundary returned error: %v", err)
	}
	aboveBoundary, err := Calculate(9, 1, 1)
	if err != nil {
		t.Fatalf("Calculate above boundary returned error: %v", err)
	}
	if !atBoundary.Pass || aboveBoundary.Pass {
		t.Fatalf("pass values = %v, %v; want true, false", atBoundary.Pass, aboveBoundary.Pass)
	}
}

func TestCalculateRejectsZeroExecutableUnits(t *testing.T) {
	if _, err := Calculate(2, 0, 0); err == nil {
		t.Fatal("Calculate accepted total zero for a known numeric CRAP value")
	}
}

func TestCalculateRejectsInvalidCounts(t *testing.T) {
	cases := [][3]int64{
		{0, 0, 1},
		{1, -1, 1},
		{1, 2, 1},
		{1, 0, -1},
	}
	for _, counts := range cases {
		if _, err := Calculate(counts[0], counts[1], counts[2]); err == nil {
			t.Fatalf("Calculate%v succeeded, want error", counts)
		}
	}
}

func TestCanonicalDecimalRoundsHalfToEvenAtTwelvePlaces(t *testing.T) {
	cases := []struct {
		numerator   string
		denominator string
		want        string
	}{
		{"1", "3", "0.333333333333"},
		{"2469135780249", "2000000000000", "1.234567890124"},
		{"2469135780251", "2000000000000", "1.234567890126"},
		{"1999999999999", "2000000000000", "1"},
	}
	for _, testCase := range cases {
		got, err := RenderCanonicalDecimal(testCase.numerator, testCase.denominator)
		if err != nil {
			t.Fatalf("RenderCanonicalDecimal(%s/%s): %v", testCase.numerator, testCase.denominator, err)
		}
		if got != testCase.want {
			t.Fatalf("RenderCanonicalDecimal(%s/%s) = %q, want %q", testCase.numerator, testCase.denominator, got, testCase.want)
		}
	}
}
