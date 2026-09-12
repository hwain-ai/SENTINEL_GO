package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestMutantTimeoutCLIParsesOnlyMutationCommands(t *testing.T) {
	for _, command := range []string{"mutation", "check", "all"} {
		for _, value := range []string{"1", "1250", "600000"} {
			if _, err := Parse([]string{command, "--mutant-timeout-ms", value}); err != nil {
				t.Errorf("command=%s value=%s error=%v", command, value, err)
			}
		}
	}
}

func TestMutantTimeoutCLIRejectsInvalidBeforeProjectAccess(t *testing.T) {
	cases := [][]string{
		{"mutation", "--mutant-timeout-ms"},
		{"mutation", "--mutant-timeout-ms", "1", "--mutant-timeout-ms", "2"},
		{"crap", "--mutant-timeout-ms", "1"},
		{"doctor", "--mutant-timeout-ms", "1"},
		{"history", "--mutant-timeout-ms", "1"},
	}
	for _, value := range []string{"", "0", "-1", "600001", "86400000", "9223372036854775808", "1.5", "1ms", "--format"} {
		cases = append(cases, []string{"mutation", "--mutant-timeout-ms", value})
	}
	for _, arguments := range cases {
		t.Run(strings.Join(arguments, " "), func(t *testing.T) {
			project := t.TempDir()
			arguments = append([]string{arguments[0], "--project", project}, arguments[1:]...)
			var stdout, stderr bytes.Buffer
			if exit := Main(arguments, &stdout, &stderr); exit != ExitUsageConfigError || stdout.Len() != 0 || !strings.Contains(stderr.String(), "--mutant-timeout-ms") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
			if _, err := os.Stat(filepath.Join(project, ".sentinel")); !os.IsNotExist(err) {
				t.Fatalf("invalid option touched state: %v", err)
			}
		})
	}
}

func TestMutantTimeoutCLIForwardsExactBridgeArguments(t *testing.T) {
	project := t.TempDir()
	source := "package sample\nfunc Positive(x int) bool { return x > 0 }\n"
	writeCLITestFile(t, filepath.Join(project, "go.mod"), "module example.test/sample\n\ngo 1.27.0\n")
	writeCLITestFile(t, filepath.Join(project, "value.go"), source)
	report := fmt.Sprintf(`{"schemaVersion":"sentinel-mutate4go-report-v1","backendName":"mutate4go","backendCommit":"9016c7adafc1c7e282b5e27768e732e477713af8","sourceInventory":[{"path":"value.go","sha256":"%x","candidateCount":1}],"candidates":[{"id":"candidate","sourceFile":"value.go","line":2,"column":1,"operator":"> -> >="}],"outcomes":[{"candidateId":"candidate","status":"killed","durationNanos":1}]}`, sha256.Sum256([]byte(source)))
	argv := filepath.Join(t.TempDir(), "argv")
	backend := cliTestExecutable(t, fmt.Sprintf("/usr/bin/printf '%%s\\n' \"$@\" > %q\n/usr/bin/printf '%%s\\n' '%s'", argv, report))
	previousGo, previousRunner := lockedGoBinary, lockedRunnerBinary
	lockedGoBinary, lockedRunnerBinary = filepath.Join(runtime.GOROOT(), "bin", "go"), backend
	t.Cleanup(func() { lockedGoBinary, lockedRunnerBinary = previousGo, previousRunner })
	for _, value := range []string{"", "1250"} {
		t.Run("value="+value, func(t *testing.T) {
			arguments := []string{"mutation", "--project", project, "--backend", backend}
			if value != "" {
				arguments = append(arguments, "--mutant-timeout-ms", value)
			}
			options, err := Parse(arguments)
			if err != nil {
				t.Fatal(err)
			}
			if report, exit, err := runMutationComponent(options, project); err != nil || exit != ExitPassed || report == nil {
				t.Fatalf("report=%+v exit=%d error=%v", report, exit, err)
			}
			want := []string{"--project-root", ".", "--go-binary", lockedGoBinary, "--runner-binary", backend, "--timeout-ms", "600000", "--source", "value.go"}
			if value != "" {
				want = append(want, "--mutant-timeout-ms", value)
			}
			got := strings.Split(strings.TrimSpace(string(mustReadCLITestFile(t, argv))), "\n")
			if !slices.Equal(got, want) {
				t.Fatalf("arguments=%q want=%q", got, want)
			}
		})
	}
}

func TestMutantTimeoutCLIHelpKeepsNoProjectAccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := Main([]string{"mutation", "--project", "/missing", "--mutant-timeout-ms", "bad", "--help"}, &stdout, &stderr)
	if exit != ExitPassed || stderr.Len() != 0 || !strings.Contains(stdout.String(), "--mutant-timeout-ms") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}
