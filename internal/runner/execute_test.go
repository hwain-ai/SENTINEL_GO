package runner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClassifiedRunStatusUsesTypedPrecedence(t *testing.T) {
	inventory, private, official := completePassedEvents()
	cases := []struct {
		name        string
		private     []PrivateEvent
		official    []OfficialEvent
		runErr      error
		timedOut    bool
		cached      bool
		buildFailed bool
		want        Status
	}{
		{name: "passed", private: private, official: official, want: StatusPassed},
		{name: "timeout", timedOut: true, want: StatusTimedOut},
		{name: "failed-build", buildFailed: true, want: StatusCompileError},
		{name: "cached", cached: true, want: StatusToolError},
		{name: "compile-without-events", runErr: errors.New("exit"), want: StatusCompileError},
		{name: "exit-disagrees-with-pass", private: private, official: official, runErr: errors.New("exit"), want: StatusToolError},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := classifiedRunStatus("nonce", inventory, testCase.private, testCase.official, testCase.runErr, testCase.timedOut, testCase.cached, testCase.buildFailed)
			if got != testCase.want {
				t.Fatalf("status = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestBoundedBufferRecordsOverflowWithoutShortWrite(t *testing.T) {
	buffer := boundedBuffer{limit: 3}
	if count, err := buffer.Write([]byte("ab")); err != nil || count != 2 {
		t.Fatalf("first write count=%d error=%v", count, err)
	}
	if count, err := buffer.Write([]byte("cde")); err != nil || count != 3 {
		t.Fatalf("overflow write count=%d error=%v", count, err)
	}
	if count, err := buffer.Write([]byte("f")); err != nil || count != 1 {
		t.Fatalf("full write count=%d error=%v", count, err)
	}
	if got := buffer.String(); got != "abc" || !buffer.exceeded {
		t.Fatalf("buffer=%q exceeded=%v", got, buffer.exceeded)
	}
}

func TestValidateRunnerRequestRejectsUnsafeRootAndTimeout(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	valid := Request{ProjectRoot: root, GoBinary: "/bin/false", Timeout: time.Second}
	for _, request := range []Request{
		{ProjectRoot: alias, GoBinary: valid.GoBinary, Timeout: valid.Timeout},
		{ProjectRoot: filepath.Join(root, "missing"), GoBinary: valid.GoBinary, Timeout: valid.Timeout},
		{ProjectRoot: root, GoBinary: valid.GoBinary},
		{ProjectRoot: root, GoBinary: valid.GoBinary, Timeout: 24*time.Hour + time.Nanosecond},
	} {
		if _, err := validateRunnerRequest(request); err == nil {
			t.Fatalf("unsafe request was accepted: %#v", request)
		}
	}
	got, err := validateRunnerRequest(valid)
	if err != nil || got.ProjectRoot != root || !filepath.IsAbs(got.GoBinary) {
		t.Fatalf("request=%#v error=%v", got, err)
	}
}

func TestClassifyExamplePassAndInvalidFailure(t *testing.T) {
	inventory := []InventoryEntry{{ID: "example.test/p.ExampleValue", Kind: KindExample}}
	private := []PrivateEvent{
		{Nonce: "nonce", TestID: inventory[0].ID, Kind: EventStart},
		{Nonce: "nonce", TestID: inventory[0].ID, Kind: EventNormalReturn},
		{Nonce: "nonce", Kind: EventMainStart},
		{Nonce: "nonce", Kind: EventMainRunReturned},
		{Nonce: "nonce", Kind: EventMainNormalReturn},
	}
	if got := Classify("nonce", inventory, private, []OfficialEvent{{TestID: inventory[0].ID, Action: OfficialRun}, {TestID: inventory[0].ID, Action: OfficialPass}}); got != StatusPassed {
		t.Fatalf("passing example status = %q", got)
	}
	private[1].Kind = EventAssertionFailure
	if got := Classify("nonce", inventory, private, []OfficialEvent{{TestID: inventory[0].ID, Action: OfficialRun}, {TestID: inventory[0].ID, Action: OfficialFail}}); got != StatusRuntimeError {
		t.Fatalf("invalid example failure status = %q", got)
	}
}

func completePassedEvents() ([]InventoryEntry, []PrivateEvent, []OfficialEvent) {
	inventory := []InventoryEntry{{ID: "example.test/p.TestValue", Kind: KindTest}}
	private := []PrivateEvent{
		{Nonce: "nonce", TestID: inventory[0].ID, Kind: EventStart},
		{Nonce: "nonce", TestID: inventory[0].ID, Kind: EventNormalReturn},
		{Nonce: "nonce", Kind: EventMainStart},
		{Nonce: "nonce", Kind: EventMainRunReturned},
		{Nonce: "nonce", Kind: EventMainNormalReturn},
	}
	official := []OfficialEvent{{TestID: inventory[0].ID, Action: OfficialRun}, {TestID: inventory[0].ID, Action: OfficialPass}}
	return inventory, private, official
}
