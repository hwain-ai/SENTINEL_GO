package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func replayFixture(t *testing.T, source string) Report {
	t.Helper()
	root, testPath, original := runnerSourceFixture(t, source)
	report, err := Execute(context.Background(), Request{ProjectRoot: root,
		GoBinary: filepath.Join(runtime.GOROOT(), "bin/go"), Timeout: 20 * time.Second, CaptureReplay: true})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(testPath)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("replay capture changed test source")
	}
	return report
}

func TestReplayIdentityDistinguishesSameCountDifferentTestsAndAssertions(t *testing.T) {
	firstSource := `package runner
import "testing"
func TestFirst(t *testing.T) { t.Fatal("private assertion message") }
func TestSecond(t *testing.T) {}
`
	first := replayFixture(t, firstSource)
	second := replayFixture(t, firstSource)
	if first.Replay == nil || second.Replay == nil || *first.Replay != *second.Replay || first.Nonce == second.Nonce {
		t.Fatalf("replay identities are not stable: %#v %#v", first, second)
	}
	differentTest := replayFixture(t, strings.ReplaceAll(firstSource, "TestFirst", "TestRenamed"))
	if differentTest.Replay == nil || first.Replay.InventorySHA256 == differentTest.Replay.InventorySHA256 {
		t.Fatal("same-count renamed inventory was accepted")
	}
	differentSite := replayFixture(t, strings.Replace(firstSource, "t.Fatal", "t.Helper(); t.Fatal", 1))
	if differentSite.Replay == nil || first.Replay.FailureSHA256 == differentSite.Replay.FailureSHA256 {
		t.Fatal("a different assertion site has the same failure identity")
	}
	payload, err := json.Marshal(first)
	if err != nil || strings.Contains(string(payload), "TestFirst") || strings.Contains(string(payload), "private assertion") || strings.Contains(string(payload), "replay") {
		t.Fatal("internal replay data changed the public v1 runner report")
	}
}

func TestReplayCaptureSupportsDirectFailuresSubtestsFuzzCleanupAndExamples(t *testing.T) {
	cases := []string{
		`func TestValue(t *testing.T) { t.Fail() }`,
		`func TestValue(t *testing.T) { t.FailNow() }`,
		`func TestValue(t *testing.T) { t.Error("wrong") }`,
		`func TestValue(t *testing.T) { t.Errorf("wrong %d", 1) }`,
		`func TestValue(t *testing.T) { t.Fatal("wrong") }`,
		`func TestValue(t *testing.T) { t.Fatalf("wrong %d", 1) }`,
		`func TestValue(t *testing.T) { t.Run("child", func(t *testing.T) { t.Fail() }) }`,
		`func TestValue(t *testing.T) { t.Cleanup(func() { t.FailNow() }) }`,
		`func helper(t *testing.T) { t.Helper(); t.Fail() }; func TestValue(t *testing.T) { helper(t) }`,
		`func FuzzValue(f *testing.F) { f.Add(1); f.Fuzz(func(t *testing.T, n int) { t.Fail() }) }`,
	}
	for _, body := range cases {
		report := replayFixture(t, "package runner\nimport \"testing\"\n"+body+"\n")
		if report.Status != StatusAssertionFailure || report.Replay == nil || len(report.Replay.FailureSHA256) != 64 {
			t.Fatalf("missing assertion identity for %s: %#v", body, report)
		}
	}
	report := replayFixture(t, "package runner\nimport \"fmt\"\nfunc ExampleValue() {\nfmt.Println(1)\n// Output: 2\n}\n")
	if report.Status != StatusAssertionFailure || report.Replay == nil || len(report.Replay.FailureSHA256) != 64 {
		t.Fatalf("missing example identity: %#v", report)
	}
}

func TestReplayCaptureDoesNotGuessUninstrumentedMethodValues(t *testing.T) {
	report := replayFixture(t, `package runner
import "testing"
func TestValue(t *testing.T) { fail := t.Fail; fail() }
`)
	if report.Status != StatusToolError || report.Replay != nil {
		t.Fatalf("failure without a known call site was accepted: %#v", report)
	}
}

func TestReplayCapturePreservesDeferredFailureAndArgumentEvaluation(t *testing.T) {
	deferred := replayFixture(t, `package runner
import "testing"
func TestValue(t *testing.T) { defer t.Error("wrong") }
`)
	if deferred.Status != StatusAssertionFailure || deferred.Replay == nil {
		t.Fatalf("deferred assertion lost: %#v", deferred)
	}
	panicked := replayFixture(t, `package runner
import "testing"
func argument() string { panic("not an assertion") }
func TestValue(t *testing.T) { t.Error(argument()) }
`)
	if panicked.Status != StatusRuntimeError {
		t.Fatalf("argument panic classified as assertion: %#v", panicked)
	}
}

func TestReplayResultsIncludeDynamicSubtestInventory(t *testing.T) {
	const source = "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) { t.Run(%q, func(t *testing.T) {}) }\n"
	first := replayFixture(t, strings.Replace(source, "%q", `"first"`, 1))
	second := replayFixture(t, strings.Replace(source, "%q", `"second"`, 1))
	if first.Replay == nil || second.Replay == nil || first.Replay.ResultsSHA256 == second.Replay.ResultsSHA256 {
		t.Fatal("different passing subtests have the same replay results")
	}
}

func TestReplayCapturePreservesOlderProjectLanguageVersion(t *testing.T) {
	for _, languageVersion := range []string{"1.13", "1.17"} {
		for _, failure := range []bool{false, true} {
			t.Run(languageVersion+"/"+map[bool]string{false: "pass", true: "fail"}[failure], func(t *testing.T) {
				body := ""
				expected := StatusPassed
				if failure {
					body = `t.Error("expected assertion")`
					expected = StatusAssertionFailure
				}
				root, testPath, original := runnerSourceFixture(t, "package runner\nimport \"testing\"\nfunc TestValue(t *testing.T) {"+body+"}\n")
				modulePath := filepath.Join(root, "go.mod")
				module := []byte("module example.test/runner\n\ngo " + languageVersion + "\n")
				if err := os.WriteFile(modulePath, module, 0o600); err != nil {
					t.Fatal(err)
				}
				report, err := Execute(context.Background(), Request{ProjectRoot: root,
					GoBinary: filepath.Join(runtime.GOROOT(), "bin/go"), Timeout: 20 * time.Second, CaptureReplay: true})
				if err != nil || report.Status != expected || report.Replay == nil {
					t.Fatalf("language=%s report=%#v error=%v", languageVersion, report, err)
				}
				if failure && len(report.Replay.FailureSHA256) != 64 {
					t.Fatal("failure identity was lost")
				}
				for path, expectedBytes := range map[string][]byte{modulePath: module, testPath: original} {
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(expectedBytes, after) {
						t.Fatal("project input changed")
					}
				}
			})
		}
	}
}
