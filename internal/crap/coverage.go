package crap

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var coverProfileLine = regexp.MustCompile(`^(.+):([0-9]+)\.([0-9]+),([0-9]+)\.([0-9]+) ([0-9]+) ([0-9]+)$`)

type sourcePosition struct {
	line   int
	column int
}

type coverageSegment struct {
	start      sourcePosition
	end        sourcePosition
	statements int64
	hitCount   int64
}

// CoverProfile is a validated Go coverprofile indexed only by exact path.
type CoverProfile struct {
	mode     string
	segments map[string][]coverageSegment
}

type coverageRange struct {
	start      sourcePosition
	end        sourcePosition
	exclusions [][2]sourcePosition
}

// ParseCoverProfile validates a Go coverprofile without path suffix fallback.
func ParseCoverProfile(input io.Reader, expectedModulePath string) (*CoverProfile, error) {
	if err := validateModulePath(expectedModulePath); err != nil {
		return nil, fmt.Errorf("invalid expected module path: %w", err)
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	mode, err := parseProfileHeader(scanner)
	if err != nil {
		return nil, err
	}
	profile := &CoverProfile{mode: mode, segments: make(map[string][]coverageSegment)}
	seen := make(map[string]struct{})
	lineNumber := 1
	for scanner.Scan() {
		lineNumber++
		modulePath, segment, key, err := parseProfileLine(scanner.Text(), lineNumber, expectedModulePath, mode)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate coverprofile segment on line %d", lineNumber)
		}
		seen[key] = struct{}{}
		profile.segments[modulePath] = append(profile.segments[modulePath], segment)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read coverprofile: %w", err)
	}
	if err := validateCoverageSegments(profile.segments); err != nil {
		return nil, err
	}
	return profile, nil
}

func parseProfileHeader(scanner *bufio.Scanner) (string, error) {
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read coverprofile header: %w", err)
		}
		return "", fmt.Errorf("coverprofile is empty")
	}
	switch scanner.Text() {
	case "mode: set":
		return "set", nil
	case "mode: count":
		return "count", nil
	case "mode: atomic":
		return "atomic", nil
	default:
		return "", fmt.Errorf("invalid coverprofile mode %q", scanner.Text())
	}
}

func parseProfileLine(line string, lineNumber int, expectedModulePath, mode string) (string, coverageSegment, string, error) {
	matches := coverProfileLine.FindStringSubmatch(line)
	if matches == nil {
		return "", coverageSegment{}, "", fmt.Errorf("invalid coverprofile line %d", lineNumber)
	}
	modulePath, err := stripModulePath(matches[1], expectedModulePath)
	if err != nil {
		return "", coverageSegment{}, "", fmt.Errorf("invalid coverprofile path on line %d: %w", lineNumber, err)
	}
	values, err := parseProfileIntegers(matches[2:], lineNumber)
	if err != nil {
		return "", coverageSegment{}, "", err
	}
	start := sourcePosition{line: int(values[0]), column: int(values[1])}
	end := sourcePosition{line: int(values[2]), column: int(values[3])}
	if !validProfileInterval(start, end, values[4]) {
		return "", coverageSegment{}, "", fmt.Errorf("invalid source interval on coverprofile line %d", lineNumber)
	}
	if err := validateProfileCounts(values[4], values[5], mode, lineNumber); err != nil {
		return "", coverageSegment{}, "", err
	}
	segment := coverageSegment{start: start, end: end, statements: values[4], hitCount: values[5]}
	key := fmt.Sprintf("%s:%d.%d,%d.%d", modulePath, start.line, start.column, end.line, end.column)
	return modulePath, segment, key, nil
}

func validateProfileCounts(statements, hitCount int64, mode string, lineNumber int) error {
	if statements < 0 || statements > maximumJSONSafeInteger {
		return fmt.Errorf("statement weight must be a JSON-safe nonnegative integer on coverprofile line %d", lineNumber)
	}
	if hitCount < 0 {
		return fmt.Errorf("hit count must be nonnegative on coverprofile line %d", lineNumber)
	}
	if mode == "set" && hitCount > 1 {
		return fmt.Errorf("set mode hit count must be zero or one on coverprofile line %d", lineNumber)
	}
	return nil
}

func validProfileInterval(start, end sourcePosition, statements int64) bool {
	if !validPosition(start) || !validPosition(end) {
		return false
	}
	if start == end {
		return statements == 0
	}
	return positionLess(start, end) && statements > 0
}

func validateCoverageSegments(files map[string][]coverageSegment) error {
	for modulePath, segments := range files {
		sort.Slice(segments, func(left, right int) bool {
			return positionLess(segments[left].start, segments[right].start)
		})
		total := int64(0)
		for index, segment := range segments {
			if index > 0 && positionLess(segment.start, segments[index-1].end) {
				return fmt.Errorf("overlapping coverprofile segments for %q", modulePath)
			}
			if segment.statements > maximumJSONSafeInteger-total {
				return fmt.Errorf("coverprofile statement total exceeds JSON-safe integer for %q", modulePath)
			}
			total += segment.statements
		}
	}
	return nil
}

func stripModulePath(profilePath, expectedModulePath string) (string, error) {
	prefix := expectedModulePath + "/"
	if !strings.HasPrefix(profilePath, prefix) {
		return "", fmt.Errorf("path is outside expected module %q", expectedModulePath)
	}
	moduleRelativePath := strings.TrimPrefix(profilePath, prefix)
	if err := validateModulePath(moduleRelativePath); err != nil {
		return "", err
	}
	return moduleRelativePath, nil
}

func parseProfileIntegers(texts []string, lineNumber int) ([]int64, error) {
	values := make([]int64, 0, len(texts))
	for _, text := range texts {
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid integer on coverprofile line %d: %w", lineNumber, err)
		}
		values = append(values, value)
	}
	return values, nil
}

// Counts returns statement-weighted covered and total units for an exact file
// and source range. Unknown reasons are returned instead of guessed data.
func (profile *CoverProfile) Counts(modulePath string, callable SourceRange, excluded []SourceRange) (covered, total int64, unknownReason string) {
	if profile == nil {
		return 0, 0, "invalidCoverageInput"
	}
	if validateModulePath(modulePath) != nil {
		return 0, 0, "invalidCoverageInput"
	}
	segments, exists := profile.segments[modulePath]
	if !exists {
		return 0, 0, "coverageFileMissing"
	}
	selectedRange, ok := newCoverageRange(callable, excluded)
	if !ok {
		return 0, 0, "invalidSourceRange"
	}
	return countCoverageSegments(segments, selectedRange)
}

func countCoverageSegments(segments []coverageSegment, selectedRange coverageRange) (covered, total int64, unknownReason string) {
	for _, segment := range segments {
		if !selectedRange.includes(segment) {
			continue
		}
		var added bool
		covered, total, added = addCoverageCounts(covered, total, segment)
		if !added {
			return 0, 0, "coverageCountOverflow"
		}
	}
	return finalizeCoverageCounts(covered, total)
}

func finalizeCoverageCounts(covered, total int64) (int64, int64, string) {
	if total == 0 {
		return 0, 0, "zeroExecutableUnits"
	}
	return covered, total, ""
}

func newCoverageRange(callable SourceRange, excluded []SourceRange) (coverageRange, bool) {
	start, end, positioned := positionsForRange(callable)
	if !positioned {
		return coverageRange{}, false
	}
	selection := coverageRange{start: start, end: end}
	for _, exclusion := range excluded {
		exclusionStart, exclusionEnd, valid := positionsForRange(exclusion)
		if !valid || !intervalContains(start, end, exclusionStart, exclusionEnd) {
			return coverageRange{}, false
		}
		selection.exclusions = append(selection.exclusions, [2]sourcePosition{exclusionStart, exclusionEnd})
	}
	return selection, true
}

func (selection coverageRange) includes(segment coverageSegment) bool {
	if !intervalContains(selection.start, selection.end, segment.start, segment.end) {
		return false
	}
	for _, exclusion := range selection.exclusions {
		if intervalContains(exclusion[0], exclusion[1], segment.start, segment.end) {
			return false
		}
	}
	return true
}

func addCoverageCounts(covered, total int64, segment coverageSegment) (int64, int64, bool) {
	if segment.statements > maximumJSONSafeInteger-total {
		return 0, 0, false
	}
	total += segment.statements
	if segment.hitCount > 0 {
		covered += segment.statements
	}
	return covered, total, true
}

func positionsForRange(sourceRange SourceRange) (sourcePosition, sourcePosition, bool) {
	start := sourcePosition{line: sourceRange.StartLine, column: sourceRange.StartColumn}
	end := sourcePosition{line: sourceRange.EndLine, column: sourceRange.EndColumn}
	if !validPosition(start) || !validPosition(end) || !positionLess(start, end) {
		return sourcePosition{}, sourcePosition{}, false
	}
	return start, end, true
}

func validPosition(position sourcePosition) bool {
	return position.line > 0 && position.column > 0
}

func positionLess(left, right sourcePosition) bool {
	return left.line < right.line || (left.line == right.line && left.column < right.column)
}

func intervalContains(outerStart, outerEnd, innerStart, innerEnd sourcePosition) bool {
	startsInside := outerStart == innerStart || positionLess(outerStart, innerStart)
	endsInside := outerEnd == innerEnd || positionLess(innerEnd, outerEnd)
	return startsInside && endsInside
}
