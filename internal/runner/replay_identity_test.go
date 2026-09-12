package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReplayIdentityRejectsContradictoryAssertionEvents(t *testing.T) {
	entry := InventoryEntry{ID: "p.TestValue", Kind: KindTest}
	passed := []byte("{\"Package\":\"p\",\"Test\":\"TestValue\",\"Action\":\"pass\"}\n")
	site := PrivateEvent{TestID: entry.ID, SiteID: "site"}
	if captureReplayIdentity([]InventoryEntry{entry}, []PrivateEvent{site}, passed, StatusPassed) != nil {
		t.Fatal("a passing test with a recorded failure was accepted")
	}
	failed := []byte("{\"Package\":\"p\",\"Test\":\"TestValue\",\"Action\":\"fail\"}\n")
	passingSibling := PrivateEvent{TestID: entry.ID + "/passing", SiteID: "other"}
	if captureReplayIdentity([]InventoryEntry{entry}, []PrivateEvent{site, passingSibling}, failed, StatusAssertionFailure) != nil {
		t.Fatal("assertion on an unfailed subtest was accepted")
	}
}

func TestPrivateAssertionEventsMustMatchNonceSiteAndInventory(t *testing.T) {
	plan := &instrumentation{captureReplay: true, inventory: []InventoryEntry{{ID: "p.TestValue", Kind: KindTest}}, sites: map[string]string{"site": "p"}}
	valid := PrivateEvent{Nonce: "nonce", TestID: "p.TestValue", Kind: EventAssertionSite, SiteID: "site"}
	cases := []PrivateEvent{
		{Nonce: "stale", TestID: valid.TestID, Kind: valid.Kind, SiteID: valid.SiteID},
		{Nonce: valid.Nonce, TestID: "p.TestOther", Kind: valid.Kind, SiteID: valid.SiteID},
		{Nonce: valid.Nonce, TestID: valid.TestID, Kind: valid.Kind, SiteID: "unknown"},
		{Nonce: valid.Nonce, TestID: valid.TestID, Kind: EventStart, SiteID: valid.SiteID},
	}
	for _, event := range cases {
		if _, _, err := separateAssertions("nonce", []PrivateEvent{event}, plan); err == nil {
			t.Fatalf("accepted %#v", event)
		}
	}
	assertions, terminal, err := separateAssertions("nonce", []PrivateEvent{valid, {Nonce: "nonce", Kind: EventMainStart}}, plan)
	if err != nil || len(assertions) != 1 || len(terminal) != 1 {
		t.Fatal("valid event split failed")
	}
	plan.captureReplay = false
	if _, _, err := separateAssertions("nonce", []PrivateEvent{valid}, plan); err == nil {
		t.Fatal("accepted site outside opt-in capture")
	}
}

func TestReplayIdentityIsOrderIndependentButIncludesRepeatedCalls(t *testing.T) {
	inventory := []InventoryEntry{{ID: "p.TestOne", Kind: KindTest}, {ID: "p.TestTwo", Kind: KindTest}}
	first := []PrivateEvent{{TestID: "p.TestOne", SiteID: "a"}, {TestID: "p.TestTwo", SiteID: "b"}}
	second := []PrivateEvent{first[1], first[0]}
	failed := []string{"p.TestOne", "p.TestTwo"}
	if failureIdentity(inventory, first, failed) != failureIdentity(inventory, second, []string{failed[1], failed[0]}) {
		t.Fatal("parallel event ordering changes identity")
	}
	if failureIdentity(inventory, first, failed) == failureIdentity(inventory, append(second, first[0]), failed) {
		t.Fatal("repeated calls disappeared from identity")
	}
	if failureIdentity(inventory, first, nil) != "" {
		t.Fatal("empty failure accepted")
	}
	if captureReplayIdentity(inventory, nil, []byte("not json"), StatusPassed) != nil {
		t.Fatal("bad official events accepted")
	}
	if failureIdentity(inventory, nil, failed) != "" {
		t.Fatal("missing call sites accepted")
	}
}

func TestReplayCaptureIgnoresShadowedNonTestingReceivers(t *testing.T) {
	report := replayFixture(t, `package runner
import "testing"
type harmless struct{}
func (harmless) Error() {}
func TestValue(t *testing.T) { { t := harmless{}; t.Error() }; t.Log("passed") }
`)
	if report.Status != StatusPassed || report.Replay == nil || report.Replay.FailureSHA256 != "" {
		t.Fatalf("shadowed receiver was treated as assertion: %#v", report)
	}
}

func TestReplayCaptureBindsTestingTBHelper(t *testing.T) {
	report := replayFixture(t, `package runner
import "testing"
func helper(t testing.TB) { t.Fail() }
func TestValue(t *testing.T) { helper(t) }
`)
	if report.Replay == nil || report.Status != StatusAssertionFailure {
		t.Fatal("testing.TB parameter not captured")
	}
	payload, err := json.Marshal(report)
	if err != nil || strings.Contains(string(payload), "SHA256") {
		t.Fatal("internal fingerprints leaked to v1 protocol")
	}
}

func TestReplayCaptureBindsHelperOnlyFileAndRestoresIt(t *testing.T) {
	root, _, _ := runnerSourceFixture(t, "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) { helper(t) }\n")
	helper := "package runner\nimport \"testing\"\nfunc helper(t *testing.T) { t.Fail() }\n"
	path := filepath.Join(root, "helper_test.go")
	if err := os.WriteFile(path, []byte(helper), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Execute(context.Background(), Request{ProjectRoot: root, GoBinary: filepath.Join(runtime.GOROOT(), "bin/go"),
		Timeout: 20 * time.Second, CaptureReplay: true})
	if err != nil || report.Status != StatusAssertionFailure || report.Replay == nil {
		t.Fatalf("helper not captured: %#v %v", report, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != helper {
		t.Fatal("helper file was not restored")
	}
}

func TestReplayCapturePreservesSoleMultiValueArguments(t *testing.T) {
	for _, method := range []string{"Error", "Errorf", "Fatal", "Fatalf"} {
		source := "package runner\nimport \"testing\"\nfunc pair() (string, int) { return \"value %d\", 1 }\n" +
			"func TestValue(t *testing.T) { t." + method + "(pair()) }\n"
		root, _, _ := runnerSourceFixture(t, source)
		request := Request{ProjectRoot: root, GoBinary: filepath.Join(runtime.GOROOT(), "bin/go"), Timeout: 20 * time.Second}
		before, err := Execute(context.Background(), request)
		// Go 1.27's default printf vet rejects the format variants' tuple form.
		want := StatusAssertionFailure
		if strings.HasSuffix(method, "f") {
			want = StatusCompileError
		}
		if err != nil || before.Status != want {
			t.Fatalf("original %s has an unexpected status: %#v %v", method, before, err)
		}
		request.CaptureReplay = true
		after, err := Execute(context.Background(), request)
		if err != nil || after.Status != before.Status || after.Replay == nil {
			t.Fatalf("%s tuple semantics changed: %#v %v", method, after, err)
		}
	}
}

func TestReplayCapturePreservesOriginalPrintfVetFailures(t *testing.T) {
	for _, method := range []string{"Errorf", "Fatalf"} {
		source := "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) { if false { t." + method + "(\"%d\", \"wrong\") } }\n"
		root, _, _ := runnerSourceFixture(t, source)
		request := Request{ProjectRoot: root, GoBinary: filepath.Join(runtime.GOROOT(), "bin/go"), Timeout: 20 * time.Second}
		before, err := Execute(context.Background(), request)
		if err != nil || before.Status != StatusCompileError {
			t.Fatalf("original vet did not reject %s: %#v %v", method, before, err)
		}
		request.CaptureReplay = true
		after, err := Execute(context.Background(), request)
		if err != nil || after.Status != StatusCompileError {
			t.Fatalf("instrumentation hid %s vet failure: %#v %v", method, after, err)
		}
	}
}

func TestReplayCaptureDoesNotInjectMainIntoHelperOnlyPackage(t *testing.T) {
	root, _, _ := runnerSourceFixture(t, "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) {}\n")
	if err := os.Mkdir(filepath.Join(root, "helpers"), 0o700); err != nil {
		t.Fatal(err)
	}
	source := "package helpers\nimport \"testing\"\nfunc helper(t *testing.T) { t.Fail() }\n"
	path := filepath.Join(root, "helpers/helper_test.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	request := Request{ProjectRoot: root, GoBinary: filepath.Join(runtime.GOROOT(), "bin/go"), Timeout: 20 * time.Second}
	before, err := Execute(context.Background(), request)
	if err != nil || before.Status != StatusPassed {
		t.Fatalf("original failed: %#v %v", before, err)
	}
	request.CaptureReplay = true
	after, err := Execute(context.Background(), request)
	if err != nil || after.Status != StatusPassed || after.Replay == nil {
		t.Fatalf("helper-only package changed classification: %#v %v", after, err)
	}
	payload, err := os.ReadFile(path)
	if err != nil || string(payload) != source {
		t.Fatal("helper-only source changed")
	}
}
