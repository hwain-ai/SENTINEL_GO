package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hwain-hwang/sentinel-go/internal/history"
	"github.com/hwain-hwang/sentinel-go/internal/mutation"
	"github.com/hwain-hwang/sentinel-go/internal/orchestrator"
)

func TestParseSupportsContractCommandsAndAllAlias(t *testing.T) {
	for _, command := range []string{"crap", "mutation", "check", "all", "doctor", "history"} {
		options, err := Parse([]string{command, "--project", "/tmp/project", "--format", "json"})
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		want := command
		if command == "all" {
			want = "check"
		}
		if options.Command != want {
			t.Fatalf("%s parsed as %q", command, options.Command)
		}
	}
}

func TestHelpDoesNotCreateProjectState(t *testing.T) {
	project := t.TempDir()
	var stdout, stderr bytes.Buffer
	exitCode := Main([]string{"--help", "--project", project}, &stdout, &stderr)
	if exitCode != 0 || stderr.Len() != 0 || stdout.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".sentinel")); !os.IsNotExist(err) {
		t.Fatalf("help created state: %v", err)
	}
}

func TestVersionIsReadOnlyAndUsesFixedDevelopmentIdentity(t *testing.T) {
	project := t.TempDir()
	var stdout, stderr bytes.Buffer
	exitCode := Main([]string{"--version"}, &stdout, &stderr)
	if exitCode != ExitPassed || stdout.String() != "sentinel-go/0.1.0\n" || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".sentinel")); !os.IsNotExist(err) {
		t.Fatalf("version created state: %v", err)
	}
}

func TestHistoryWithoutStateIsReadOnly(t *testing.T) {
	project := t.TempDir()
	var stdout, stderr bytes.Buffer
	exitCode := Main([]string{"history", "--project", project, "--format", "json"}, &stdout, &stderr)
	if exitCode != ExitPassed || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".sentinel")); !os.IsNotExist(err) {
		t.Fatalf("history created state: %v", err)
	}
}

func TestInvalidCommandUsesUsageConfigExit(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := Main([]string{"magic"}, &stdout, &stderr); exitCode != ExitUsageConfigError {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestParseRejectsOptionsOutsideTheirCommand(t *testing.T) {
	cases := [][]string{
		{"doctor", "--coverprofile", "coverage.out"},
		{"crap", "--source", "value.go"},
		{"crap", "--repeated"},
	}
	for _, arguments := range cases {
		if _, err := Parse(arguments); err == nil {
			t.Fatalf("Parse(%q) succeeded", arguments)
		}
	}
}

func TestPartialMutationSourceInventoryFailsBeforeStateOrBackendAccess(t *testing.T) {
	project := t.TempDir()
	writeCLITestFile(t, filepath.Join(project, "first.go"), "package sample\n")
	writeCLITestFile(t, filepath.Join(project, "last.go"), "package sample\n")
	var stdout, stderr bytes.Buffer

	exitCode := Main([]string{
		"mutation",
		"--project", project,
		"--source", "first.go",
		"--backend", filepath.Join(project, "missing-backend"),
		"--format", "json",
	}, &stdout, &stderr)

	if exitCode != ExitUsageConfigError {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".sentinel")); !os.IsNotExist(err) {
		t.Fatalf("partial inventory created state: %v", err)
	}
}

func TestDoctorChecksExactBridgeVersionWithoutCreatingState(t *testing.T) {
	project := t.TempDir()
	backend := cliTestExecutable(t, `/usr/bin/printf '%s\n' 'sentinel-mutate4go-bridge/1 upstream/9016c7adafc1c7e282b5e27768e732e477713af8'`)
	runnerBinary := cliTestExecutable(t, `/usr/bin/printf '%s\n' 'sentinel-go-test-runner/1'`)
	originalRunner := lockedRunnerBinary
	originalBackendDigest := lockedBackendSHA256
	originalRunnerDigest := lockedRunnerSHA256
	lockedRunnerBinary = runnerBinary
	lockedBackendSHA256 = fmt.Sprintf("%x", sha256.Sum256(mustReadCLITestFile(t, backend)))
	lockedRunnerSHA256 = fmt.Sprintf("%x", sha256.Sum256(mustReadCLITestFile(t, runnerBinary)))
	defer func() {
		lockedRunnerBinary = originalRunner
		lockedBackendSHA256 = originalBackendDigest
		lockedRunnerSHA256 = originalRunnerDigest
	}()
	var stdout, stderr bytes.Buffer
	exitCode := Main([]string{"doctor", "--project", project, "--backend", backend, "--format", "json"}, &stdout, &stderr)
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".sentinel")); !os.IsNotExist(err) {
		t.Fatalf("doctor created state: %v", err)
	}

	wrong := cliTestExecutable(t, `/usr/bin/printf '%s\n' 'wrong-version'`)
	lockedBackendSHA256 = fmt.Sprintf("%x", sha256.Sum256(mustReadCLITestFile(t, wrong)))
	stdout.Reset()
	stderr.Reset()
	if exitCode := Main([]string{"doctor", "--project", project, "--backend", wrong}, &stdout, &stderr); exitCode != ExitDependencyError {
		t.Fatalf("wrong-version exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestDoctorRejectsSameVersionCompanionBeforeExecutingDigestMismatch(t *testing.T) {
	project := t.TempDir()
	marker := filepath.Join(t.TempDir(), "executed")
	backend := cliTestExecutable(t, fmt.Sprintf(
		`/usr/bin/touch %q
/usr/bin/printf '%%s\n' 'sentinel-mutate4go-bridge/1 upstream/9016c7adafc1c7e282b5e27768e732e477713af8'`,
		marker,
	))
	runnerBinary := cliTestExecutable(t, `/usr/bin/printf '%s\n' 'sentinel-go-test-runner/1'`)
	originalRunner := lockedRunnerBinary
	originalBackendDigest := lockedBackendSHA256
	originalRunnerDigest := lockedRunnerSHA256
	lockedRunnerBinary = runnerBinary
	lockedBackendSHA256 = strings.Repeat("0", 64)
	lockedRunnerSHA256 = fmt.Sprintf("%x", sha256.Sum256(mustReadCLITestFile(t, runnerBinary)))
	defer func() {
		lockedRunnerBinary = originalRunner
		lockedBackendSHA256 = originalBackendDigest
		lockedRunnerSHA256 = originalRunnerDigest
	}()

	var stdout, stderr bytes.Buffer
	exitCode := Main([]string{"doctor", "--project", project, "--backend", backend}, &stdout, &stderr)
	if exitCode != ExitDependencyError {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("digest-mismatched backend executed: %v", err)
	}
}

func TestCompanionLayoutUsesSiblingLibexec(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "bin", "sentinel-go")
	if got := companionPath(executable, "sentinel-mutate4go-bridge"); got != filepath.Join(root, "libexec", "sentinel-mutate4go-bridge") {
		t.Fatalf("backend path=%q", got)
	}
	if got := companionPath(executable, "sentinel-go-test-runner"); got != filepath.Join(root, "libexec", "sentinel-go-test-runner") {
		t.Fatalf("runner path=%q", got)
	}
}

func TestCrapFailuresCreatePrivacySafeRepeatedHistory(t *testing.T) {
	project := t.TempDir()
	writeCLITestFile(t, filepath.Join(project, "go.mod"), "module example.test/private\n\ngo 1.27.0\n")
	writeCLITestFile(t, filepath.Join(project, "value.go"), "package private\n\nfunc Value() int { return 1 }\n")
	profile := filepath.Join(t.TempDir(), "coverage.out")
	writeCLITestFile(t, profile, "mode: set\nexample.test/private/other.go:1.1,1.2 1 1\n")

	for run := 0; run < 2; run++ {
		var stdout, stderr bytes.Buffer
		exitCode := Main([]string{"crap", "--project", project, "--coverprofile", profile, "--format", "json"}, &stdout, &stderr)
		if exitCode != ExitQualityFailed {
			t.Fatalf("run %d exit=%d stdout=%q stderr=%q", run, exitCode, stdout.String(), stderr.String())
		}
	}

	var stdout, stderr bytes.Buffer
	if exitCode := Main([]string{"history", "--project", project, "--repeated", "--format", "json"}, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("history exit=%d stderr=%q", exitCode, stderr.String())
	}
	var result struct {
		History struct {
			RepeatedDefects []struct {
				ObservationCount int  `json:"observationCount"`
				Repeated         bool `json:"repeated"`
			} `json:"repeatedDefects"`
		} `json:"history"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.History.RepeatedDefects) != 1 || result.History.RepeatedDefects[0].ObservationCount != 2 || !result.History.RepeatedDefects[0].Repeated {
		t.Fatalf("history = %s", stdout.String())
	}

	historyRoot := filepath.Join(project, ".sentinel", "state-v1", "runs")
	if err := filepath.WalkDir(historyRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(payload), "value.go") || strings.Contains(string(payload), "example.test/private") {
			return fmt.Errorf("history leaked raw identity: %s", payload)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryReturnsEvidenceErrorForTamperedBundle(t *testing.T) {
	project := t.TempDir()
	writeCLITestFile(t, filepath.Join(project, "go.mod"), "module example.test/tamper\n\ngo 1.27.0\n")
	writeCLITestFile(t, filepath.Join(project, "value.go"), "package tamper\n\nfunc Value() int { return 1 }\n")
	profile := filepath.Join(t.TempDir(), "coverage.out")
	writeCLITestFile(t, profile, "mode: set\nexample.test/tamper/value.go:3.18,3.28 1 1\n")
	var stdout, stderr bytes.Buffer
	_ = Main([]string{"crap", "--project", project, "--coverprofile", profile, "--format", "json"}, &stdout, &stderr)

	runsRoot := filepath.Join(project, ".sentinel", "state-v1", "runs")
	entries, err := os.ReadDir(runsRoot)
	if err != nil || len(entries) != 1 {
		t.Fatalf("run entries=%d error=%v", len(entries), err)
	}
	evidencePath := filepath.Join(runsRoot, entries[0].Name(), "evidence.json")
	file, err := os.OpenFile(evidencePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(" "); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	tampered, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if exitCode := Main([]string{"history", "--project", project, "--format", "json"}, &stdout, &stderr); exitCode != ExitEvidenceError {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	after, err := os.ReadFile(evidencePath)
	if err != nil || !bytes.Equal(tampered, after) {
		t.Fatal("history modified tampered evidence")
	}
}

func TestCoverageProfileRunsPinnedGoWithSealedOfflineEnvironment(t *testing.T) {
	project := t.TempDir()
	writeCLITestFile(t, filepath.Join(project, "go.mod"), "module example.test/coverage\n\ngo 1.27.0\n")
	writeCLITestFile(t, filepath.Join(project, "value.go"), "package coverage\n\nfunc Value() int { return 1 }\n")
	writeCLITestFile(t, filepath.Join(project, "value_test.go"), "package coverage\n\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value() != 1 { t.Fatal(\"bad\") } }\n")
	originalGoBinary := lockedGoBinary
	lockedGoBinary = filepath.Join(runtime.GOROOT(), "bin", "go")
	defer func() { lockedGoBinary = originalGoBinary }()
	t.Setenv("GOROOT", "/attacker/go")
	t.Setenv("GOPROXY", "https://attacker.invalid")

	profile, cleanup, err := coverageProfile(Options{}, project)
	if err != nil {
		t.Fatalf("coverageProfile() error = %v", err)
	}
	defer cleanup()
	payload, err := os.ReadFile(profile)
	if err != nil || !strings.Contains(string(payload), "example.test/coverage/value.go") {
		t.Fatalf("coverage profile = %q, error = %v", payload, err)
	}
}

func TestRunMutationComponentUsesLocalStrictGate(t *testing.T) {
	project := t.TempDir()
	source := []byte("package sample\n\nfunc Positive(value int) bool { return value > 0 }\n")
	writeCLITestFile(t, filepath.Join(project, "go.mod"), "module example.test/sample\n\ngo 1.27.0\n")
	writeCLITestFile(t, filepath.Join(project, "value.go"), string(source))
	digest := sha256.Sum256(source)
	report := fmt.Sprintf(`{"schemaVersion":"sentinel-mutate4go-report-v1","backendName":"mutate4go","backendCommit":"%s","sourceInventory":[{"path":"value.go","sha256":"%x","candidateCount":1}],"candidates":[{"id":"candidate","sourceFile":"value.go","line":3,"column":40,"operator":"> -> >="}],"outcomes":[{"candidateId":"candidate","status":"killed","durationNanos":1}]}`, mutation.Mutate4GoCommit, digest)
	backend := cliTestExecutable(t, "/usr/bin/printf '%s\\n' '"+report+"'")
	originalRunner := lockedRunnerBinary
	lockedRunnerBinary = backend
	defer func() { lockedRunnerBinary = originalRunner }()

	got, exitCode, err := runMutationComponent(Options{Backend: backend, Sources: []string{"value.go"}}, project)
	if err != nil || exitCode != ExitPassed || got == nil || !got.Gate.Pass {
		t.Fatalf("report=%#v exit=%d error=%v", got, exitCode, err)
	}
}

func TestFindingsForResultIncludesOnlyUnknownFailedAndNonKilledRows(t *testing.T) {
	result := commandResult{
		Crap: &orchestrator.CrapReport{Rows: []orchestrator.CrapResultRow{
			{CallableID: "known-pass", Pass: true},
			{CallableID: "known-fail"},
			{CallableID: "unknown", UnknownReason: "coverageMissing"},
		}},
		Mutation: &orchestrator.MutationReport{Mutants: []orchestrator.PublicMutant{
			{CandidateID: "killed", Status: mutation.Killed},
			{CandidateID: "survived", Status: mutation.Survived},
		}},
	}
	store := history.NewStore(t.TempDir())
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	findings, err := findingsForResult(store, result)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 3 {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestQualityComponentsPreserveExactCrapMaximumAndAllMutationStates(t *testing.T) {
	counts := map[mutation.Status]int64{
		mutation.Killed: 1, mutation.Survived: 1, mutation.Uncovered: 0,
		mutation.TimedOut: 0, mutation.CompileError: 0, mutation.RuntimeError: 0,
		mutation.Pending: 0, mutation.Ignored: 0, mutation.ToolError: 0,
	}
	result := commandResult{
		Crap: &orchestrator.CrapReport{Pass: false, Rows: []orchestrator.CrapResultRow{
			{CallableID: "unknown", UnknownReason: "coverageMissing"},
			{CallableID: "limit", Numerator: "8", Denominator: "1", Pass: true},
			{CallableID: "maximum", Numerator: "17", Denominator: "2", Pass: false},
		}},
		Mutation: &orchestrator.MutationReport{Gate: mutation.GateResult{
			Pass: false, InScope: 2, Counts: counts,
		}},
	}
	components, err := qualityComponents("check", result)
	if err != nil {
		t.Fatal(err)
	}
	if components.Crap == nil || components.Crap.CallableCount != 3 || components.Crap.UnknownCount != 1 ||
		components.Crap.MaxNumerator != "17" || components.Crap.MaxDenominator != "2" || components.Crap.Pass {
		t.Fatalf("CRAP component = %#v", components.Crap)
	}
	if components.Mutation == nil || components.Mutation.InScope != 2 || components.Mutation.Killed != 1 ||
		components.Mutation.Survived != 1 || components.Mutation.Pass {
		t.Fatalf("mutation component = %#v", components.Mutation)
	}
}

func TestMissingFailedReportGetsAValidFailClosedComponent(t *testing.T) {
	components, err := qualityComponents("crap", commandResult{})
	if err != nil {
		t.Fatal(err)
	}
	if components.Crap == nil || components.Mutation != nil || components.Crap.CallableCount != 0 ||
		components.Crap.MaxNumerator != "0" || components.Crap.MaxDenominator != "1" || components.Crap.Pass {
		t.Fatalf("components = %#v", components)
	}
}

func TestRenderTextCoversDoctorHistoryAndQualityComponents(t *testing.T) {
	cases := []struct {
		result commandResult
		want   string
	}{
		{
			result: commandResult{Doctor: &doctorResult{Pass: true, GoVersion: "go1.27.1", Backend: "mutate4go"}},
			want:   "doctor: pass=true go=go1.27.1 backend=mutate4go",
		},
		{
			result: commandResult{History: &historyResult{Runs: []history.RunRecord{{}}, RepeatedDefects: []history.RepeatedDefect{{}, {}}}},
			want:   "history: runs=1 repeated-defects=2",
		},
		{
			result: commandResult{
				Run:      runResult{Command: "check", TerminalStatus: "qualityFailed"},
				Crap:     &orchestrator.CrapReport{Pass: false, Rows: []orchestrator.CrapResultRow{{}, {}}},
				Mutation: &orchestrator.MutationReport{Gate: mutation.GateResult{Pass: false, InScope: 3}},
			},
			want: "check: qualityFailed crap[pass=false callables=2] mutation[pass=false mutants=3]",
		},
	}
	for _, testCase := range cases {
		if got := renderText(testCase.result); got != testCase.want {
			t.Fatalf("renderText() = %q, want %q", got, testCase.want)
		}
	}
}

func TestWriteJSONDoesNotEscapeAllowedDisplayCharacters(t *testing.T) {
	result := commandResult{SchemaVersion: "<schema>", Run: runResult{Command: "doctor", TerminalStatus: "passed"}}
	var output bytes.Buffer
	if err := writeCommandResult(&output, "json", result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"schemaVersion":"<schema>"`) {
		t.Fatalf("JSON was unexpectedly HTML-escaped: %s", output.String())
	}
}

func cliTestExecutable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "executable")
	writeCLITestFile(t, path, "#!/usr/bin/bash\nset -eu\n"+body+"\n")
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeCLITestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustReadCLITestFile(t *testing.T, path string) []byte {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
