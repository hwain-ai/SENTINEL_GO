package cli

import (
	"regexp"
	"testing"
)

func TestNewRunIDIsCanonicalLowercaseUUIDv4(t *testing.T) {
	runID, err := newRunID()
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(runID) {
		t.Fatalf("runId = %q", runID)
	}
}
