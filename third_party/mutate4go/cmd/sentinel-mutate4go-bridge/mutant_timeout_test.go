package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMutantTimeoutBridgeValidatesBeforeFilesystemAccess(t *testing.T) {
	cases := [][]string{
		{"--mutant-timeout-ms"},
		{"--mutant-timeout-ms", "1", "--mutant-timeout-ms", "2"},
		{"--mutant-timeout-ms=1", "--mutant-timeout-ms=2"},
		{"--mutant-timeout-ms=1", "-mutant-timeout-ms", "2"},
		{"--mutant-timeout-ms", "1001", "--timeout-ms", "1000"},
		{"--mutant-timeout-ms", "1001"},
	}
	for _, value := range []string{"0", "-1", "86400001", "9223372036854775808", "1.5", "bad"} {
		cases = append(cases, []string{"--mutant-timeout-ms", value})
	}
	for _, arguments := range cases {
		t.Run(strings.Join(arguments, " "), func(t *testing.T) {
			arguments = append([]string{"--timeout-ms", "1000", "--project-root", "/missing-project"}, arguments...)
			if _, err := parseOptions(arguments); err == nil || strings.Contains(err.Error(), "project root") || strings.Contains(err.Error(), "executable") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestMutantTimeoutBridgeAcceptsOptionalIntegerLimitInEitherOrder(t *testing.T) {
	project := bridgeFixture(t, "package sample\n", "package sample\n")
	binary := bridgeScript(t, "exit 0")
	base := []string{"--project-root", project, "--go-binary", binary, "--runner-binary", binary, "--source", "value.go"}
	for _, value := range []int{0, 1, 1250, 86400000} {
		for _, equals := range []bool{false, true} {
			for _, first := range []bool{false, true} {
				arguments := append([]string(nil), base...)
				var selected []string
				if value != 0 {
					selected = []string{"--mutant-timeout-ms", strconv.Itoa(value)}
					if equals {
						selected = []string{"-mutant-timeout-ms=" + strconv.Itoa(value)}
					}
				}
				if first {
					arguments = append(arguments, selected...)
				}
				arguments = append(arguments, "--timeout-ms", "86400000")
				if !first {
					arguments = append(arguments, selected...)
				}
				if options, err := parseOptions(arguments); err != nil || options.mutantTimeout != time.Duration(value)*time.Millisecond || options.timeout != 24*time.Hour {
					t.Errorf("arguments=%q options=%+v error=%v", arguments, options, err)
				}
			}
		}
	}
}

func TestMutantTimeoutBridgeKeepsFullReportWithActualRunner(t *testing.T) {
	source := "package sample\nfunc Positive(x int) bool { return x > 0 }\n"
	tests := `package sample
import ("testing"; "time")
func TestPositive(t *testing.T) {
  if Positive(0) { time.Sleep(6*time.Second); t.Fatal("mutant failed after hanging") }
  if !Positive(1) { t.Fatal("later candidate failed") }
  time.Sleep(2250*time.Millisecond) // Baseline/control exceeds the selected mutant limit.
}
`
	project := bridgeFixture(t, source, tests)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	exit := runContext(ctx, []string{"--project-root", project, "--go-binary", filepath.Join(runtime.GOROOT(), "bin", "go"), "--runner-binary", buildRunner(t), "--timeout-ms", "10000", "--mutant-timeout-ms", "2000", "--source", "value.go"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	var report machineReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.SourceInventory) != 1 || report.SourceInventory[0].CandidateCount != 2 || len(report.Candidates) != 2 || len(report.Outcomes) != 2 {
		t.Fatalf("incomplete report=%+v", report)
	}
	if report.Outcomes[0].Status != "timedOut" || report.Outcomes[1].Status != "killed" {
		t.Errorf("outcomes=%+v", report.Outcomes)
	}
	for index, outcome := range report.Outcomes {
		if outcome.CandidateID != report.Candidates[index].ID {
			t.Errorf("candidate/outcome mismatch at %d", index)
		}
	}
	assertMutantFixtureRestored(t, project, source, tests)
}

func TestMutantTimeoutBridgeKeepsControlsAndSeparatesEveryCommandBudget(t *testing.T) {
	for _, selected := range []time.Duration{0, 200 * time.Millisecond} {
		t.Run(selected.String(), func(t *testing.T) {
			source, tests := "package sample\nfunc Positive(x int) bool { return x > 0 }\n", "package sample\n"
			project := bridgeFixture(t, source, tests)
			calls := filepath.Join(t.TempDir(), "calls")
			passed := strings.Replace(lifecycleTimedOut, "timedOut", "passed", 1)
			runner := bridgeScript(t, fmt.Sprintf(`while [ "$#" -gt 0 ]; do
  case "$1" in
    --timeout-ms) limit="$2"; shift ;;
    --coverprofile) profile="$2"; shift ;;
  esac
  shift
done
echo "$limit" >> %q
if [ -n "${profile:-}" ]; then
  printf 'mode: set\nexample.test/bridge/value.go:2.1,2.60 1 1\n' > "$profile"
fi
if [ "$(wc -l < %q)" -le 3 ]; then sleep .25; else sleep .13; fi
printf '%%s' '%s'`, calls, calls, passed))
			options := bridgeOptions{projectRoot: project, goBinary: bridgeScript(t, "sleep .13"), runnerBinary: runner, timeout: time.Second, mutantTimeout: selected, sources: []string{"value.go"}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			report, exit, err := execute(ctx, options)
			if err != nil || exit != 0 || len(report.Outcomes) != 2 {
				t.Fatalf("report=%+v exit=%d error=%v", report, exit, err)
			}
			for _, outcome := range report.Outcomes {
				if outcome.Status != "survived" || outcome.DurationNanos <= int64(selected) {
					t.Errorf("three-command candidate batch was limited: %+v", outcome)
				}
			}
			payload, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			limit := "1000"
			if selected > 0 {
				limit = "200"
			}
			want := []string{"1000", "1000", "1000", limit, limit, limit, limit}
			if got := strings.Fields(string(payload)); !slices.Equal(got, want) {
				t.Errorf("coverage/control/replay budgets=%q want=%q", got, want)
			}
			assertMutantFixtureRestored(t, project, source, tests)
		})
	}
}

func TestMutantTimeoutBridgeRawCompileUsesSelectedBudget(t *testing.T) {
	source, tests := "package sample\nfunc Positive(x int) bool { return x > 0 }\n", "package sample\n"
	project := bridgeFixture(t, source, tests)
	_, plan, err := discoverSource(project, "example.test/bridge", "value.go", nil)
	if err != nil || len(plan) != 2 {
		t.Fatalf("plan=%+v error=%v", plan, err)
	}
	plan[0].covered = true
	marker := filepath.Join(t.TempDir(), "unexpected-replay")
	options := bridgeOptions{projectRoot: project, goBinary: bridgeScript(t, "sleep .3"), runnerBinary: bridgeScript(t, fmt.Sprintf("touch %q", marker)), timeout: time.Second, mutantTimeout: 50 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	outcome, err := executeCandidate(ctx, options, plan[0])
	if err != nil || outcome.Status != "timedOut" {
		t.Errorf("outcome=%+v error=%v", outcome, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("compile timeout started a replay: %v", err)
	}
	assertMutantFixtureRestored(t, project, source, tests)
}

func assertMutantFixtureRestored(t *testing.T, project, source, tests string) {
	t.Helper()
	for name, want := range map[string]string{"value.go": source, "value_test.go": tests} {
		got, err := os.ReadFile(filepath.Join(project, name))
		if err != nil || string(got) != want {
			t.Errorf("%s was not restored: %q error=%v", name, got, err)
		}
	}
	if matches, err := filepath.Glob(filepath.Join(project, "*sentinel*")); err != nil || len(matches) != 0 {
		t.Errorf("runner temporary files=%q error=%v", matches, err)
	}
}
