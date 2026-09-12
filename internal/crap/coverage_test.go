package crap

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestParseCoverProfileUsesExactPathsAndStatementWeights(t *testing.T) {
	profile, err := ParseCoverProfile(strings.NewReader("mode: set\nexample.com/project/internal/sample/file.go:4.2,4.20 2 1\nexample.com/project/internal/sample/file.go:5.2,5.20 3 0\n"), "example.com/project")
	if err != nil {
		t.Fatalf("ParseCoverProfile returned error: %v", err)
	}
	callable := SourceRange{StartByte: 0, EndByte: 1000, StartLine: 4, StartColumn: 1, EndLine: 6, EndColumn: 1}
	covered, total, reason := profile.Counts("internal/sample/file.go", callable, nil)
	if covered != 2 || total != 5 || reason != "" {
		t.Fatalf("Counts = (%d, %d, %q), want (2, 5, empty)", covered, total, reason)
	}
	_, _, reason = profile.Counts("file.go", callable, nil)
	if reason != "coverageFileMissing" {
		t.Fatalf("suffix-only lookup reason = %q, want coverageFileMissing", reason)
	}
}

func TestParseCoverProfileRejectsOverlappingSegments(t *testing.T) {
	payload := "mode: set\n" +
		"example.com/project/file.go:1.1,1.10 1 1\n" +
		"example.com/project/file.go:1.5,1.15 1 0\n"
	if _, err := ParseCoverProfile(strings.NewReader(payload), "example.com/project"); err == nil {
		t.Fatal("ParseCoverProfile accepted overlapping source intervals")
	}
}

func TestParseCoverProfileEnforcesModeCounts(t *testing.T) {
	setPayload := "mode: set\nexample.com/project/file.go:1.1,1.2 1 2\n"
	if _, err := ParseCoverProfile(strings.NewReader(setPayload), "example.com/project"); err == nil {
		t.Fatal("ParseCoverProfile accepted count 2 in set mode")
	}
	for _, mode := range []string{"count", "atomic"} {
		payload := "mode: " + mode + "\nexample.com/project/file.go:1.1,1.2 1 2\n"
		if _, err := ParseCoverProfile(strings.NewReader(payload), "example.com/project"); err != nil {
			t.Fatalf("ParseCoverProfile rejected count 2 in %s mode: %v", mode, err)
		}
	}
}

func TestParseCoverProfileAcceptsGoZeroWidthMarkersWithoutCountingThem(t *testing.T) {
	payload := "mode: set\n" +
		"example.com/project/file.go:1.1,1.2 1 1\n" +
		"example.com/project/file.go:1.2,1.2 0 1\n"
	profile, err := ParseCoverProfile(strings.NewReader(payload), "example.com/project")
	if err != nil {
		t.Fatalf("ParseCoverProfile rejected a Go zero-width marker: %v", err)
	}
	callable := SourceRange{StartByte: 0, EndByte: 10, StartLine: 1, StartColumn: 1, EndLine: 2, EndColumn: 1}
	covered, total, reason := profile.Counts("file.go", callable, nil)
	if covered != 1 || total != 1 || reason != "" {
		t.Fatalf("Counts = (%d, %d, %q), want (1, 1, empty)", covered, total, reason)
	}
}

func TestParseCoverProfileRejectsJSONUnsafeStatementWeight(t *testing.T) {
	payloads := []string{
		"mode: count\nexample.com/project/file.go:1.1,1.2 9007199254740992 1\n",
		"mode: count\nexample.com/project/file.go:1.1,1.2 9007199254740991 1\nexample.com/project/file.go:1.2,1.3 1 0\n",
	}
	for _, payload := range payloads {
		if _, err := ParseCoverProfile(strings.NewReader(payload), "example.com/project"); err == nil {
			t.Fatal("ParseCoverProfile accepted a JSON-unsafe statement total")
		}
	}
}

func TestCoverProfileCountsRejectsMissingSourcePositions(t *testing.T) {
	profile, err := ParseCoverProfile(strings.NewReader("mode: set\nexample.com/project/file.go:10.1,10.2 1 1\n"), "example.com/project")
	if err != nil {
		t.Fatal(err)
	}
	covered, total, reason := profile.Counts("file.go", SourceRange{StartByte: 0, EndByte: 1}, nil)
	if covered != 0 || total != 0 || reason != "invalidSourceRange" {
		t.Fatalf("Counts = (%d, %d, %q), want fail-closed invalidSourceRange", covered, total, reason)
	}
}

func TestParseCoverProfileRejectsAmbiguousOrInvalidData(t *testing.T) {
	cases := []string{
		"mode: set\nexample.com/project/./file.go:1.1,1.2 1 1\n",
		"mode: set\nexample.com/project/file.go:1.1,1.2 0 1\n",
		"mode: set\nexample.com/project/file.go:1.1,1.2 1 -1\n",
		"mode: set\nexample.com/project/file.go:2.1,1.2 1 1\n",
		"mode: set\nexample.com/project/file.go:1.1,1.2 1 1\nexample.com/project/file.go:1.1,1.2 1 1\n",
		"mode: set\nexample.com/outside/file.go:1.1,1.2 1 1\n",
		"mode: set\nexample.com/project-extra/file.go:1.1,1.2 1 1\n",
	}
	for _, payload := range cases {
		if _, err := ParseCoverProfile(strings.NewReader(payload), "example.com/project"); err == nil {
			t.Fatalf("ParseCoverProfile(%q) succeeded, want error", payload)
		}
	}
}

func TestActualGo127NestedLiteralCoverageHasOneOwnerPerSegment(t *testing.T) {
	source, err := os.ReadFile("testdata/nested_literal/fixture.go")
	if err != nil {
		t.Fatal(err)
	}
	profileBytes, err := os.ReadFile("testdata/nested_literal/go1.27.1.cover")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := AnalyzeSource(source, "internal/crap/testdata/nested_literal/fixture.go")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := ParseCoverProfile(bytes.NewReader(profileBytes), "example.com/project")
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][2]int64{
		"Nested":                 {3, 3},
		"Nested.<literal:inner>": {0, 3},
	}
	if len(rows) != len(want) {
		t.Fatalf("len(rows) = %d, want %d", len(rows), len(want))
	}
	for _, row := range rows {
		covered, total, reason := profile.Counts(row.ModulePath, row.SourceRange, row.ExcludedRanges)
		expected, exists := want[row.QualifiedName]
		if !exists {
			t.Fatalf("unexpected callable %q", row.QualifiedName)
		}
		if covered != expected[0] || total != expected[1] || reason != "" {
			t.Fatalf("%s Counts = (%d, %d, %q), want (%d, %d, empty)", row.QualifiedName, covered, total, reason, expected[0], expected[1])
		}
	}
}

func TestCoverageSegmentStraddlingSameLineCallablesIsUnknown(t *testing.T) {
	profile, err := ParseCoverProfile(strings.NewReader("mode: set\nexample.com/project/file.go:1.1,1.50 1 1\n"), "example.com/project")
	if err != nil {
		t.Fatal(err)
	}
	callables := []SourceRange{
		{StartByte: 0, EndByte: 25, StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 25},
		{StartByte: 25, EndByte: 50, StartLine: 1, StartColumn: 26, EndLine: 1, EndColumn: 50},
	}
	for _, callable := range callables {
		covered, total, reason := profile.Counts("file.go", callable, nil)
		if covered != 0 || total != 0 || reason != "zeroExecutableUnits" {
			t.Fatalf("Counts = (%d, %d, %q), want fail-closed zeroExecutableUnits", covered, total, reason)
		}
	}
}
