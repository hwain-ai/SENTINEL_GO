package runid

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewWithReaderSetsVersionVariantAndCanonicalText(t *testing.T) {
	tests := []struct {
		input byte
		want  string
	}{
		{input: 0x00, want: "00000000-0000-4000-8000-000000000000"},
		{input: 0xff, want: "ffffffff-ffff-4fff-bfff-ffffffffffff"},
	}
	for _, testCase := range tests {
		runID, err := newWithReader(bytes.NewReader(bytes.Repeat([]byte{testCase.input}, 16)))
		if err != nil {
			t.Fatal(err)
		}
		if runID != testCase.want || !Valid(runID) {
			t.Fatalf("input %x produced %q", testCase.input, runID)
		}
	}
}

func TestNewWithReaderRejectsShortEntropy(t *testing.T) {
	runID, err := newWithReader(strings.NewReader("short"))
	if err == nil || runID != "" {
		t.Fatalf("runId = %q, error = %v", runID, err)
	}
}

func TestValidRequiresExactLowercaseCanonicalUUID(t *testing.T) {
	for _, value := range []string{
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-1000-7000-000000000001",
		"00000000-0000-0000-0000-000000000000",
	} {
		if !Valid(value) {
			t.Fatalf("canonical UUID was rejected: %q", value)
		}
	}
	for _, value := range []string{
		"00000000000040008000000000000001",
		"00000000-0000-4000-8000-00000000000A",
		"00000000-0000-4000-8000-00000000001",
	} {
		if Valid(value) {
			t.Fatalf("invalid runId accepted: %q", value)
		}
	}
}
