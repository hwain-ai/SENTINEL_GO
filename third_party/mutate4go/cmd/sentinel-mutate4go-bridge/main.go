package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/unclebob/mutate4go/internal/coverage"
	"github.com/unclebob/mutate4go/internal/mutations"
)

const (
	bridgeVersion = "sentinel-mutate4go-bridge/1"
	backendCommit = "9016c7adafc1c7e282b5e27768e732e477713af8"
)

type sourceList []string

func (sources *sourceList) String() string {
	return strings.Join(*sources, ",")
}

func (sources *sourceList) Set(value string) error {
	*sources = append(*sources, value)
	return nil
}

type sourceInventory struct {
	Path           string `json:"path"`
	SHA256         string `json:"sha256"`
	CandidateCount int64  `json:"candidateCount"`
}

type machineCandidate struct {
	ID         string `json:"id"`
	SourceFile string `json:"sourceFile"`
	Line       int64  `json:"line"`
	Column     int64  `json:"column"`
	Operator   string `json:"operator"`
}

type machineOutcome struct {
	CandidateID   string `json:"candidateId"`
	Status        string `json:"status"`
	DurationNanos int64  `json:"durationNanos"`
}

type machineReport struct {
	SchemaVersion   string             `json:"schemaVersion"`
	BackendName     string             `json:"backendName"`
	BackendCommit   string             `json:"backendCommit"`
	SourceInventory []sourceInventory  `json:"sourceInventory"`
	Candidates      []machineCandidate `json:"candidates"`
	Outcomes        []machineOutcome   `json:"outcomes"`
}

type bridgeOptions struct {
	projectRoot           string
	goBinary              string
	runnerBinary          string
	referenceRunnerBinary string
	referenceRunID        string
	timeout               time.Duration
	mutantTimeout         time.Duration
	sources               []string
}

type plannedCandidate struct {
	candidate machineCandidate
	site      mutations.Site
	source    string
	covered   bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	exitCode := runContext(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(exitCode)
}

func run(arguments []string, stdout, stderr io.Writer) int {
	return runContext(context.Background(), arguments, stdout, stderr)
}

func runContext(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) == 1 && arguments[0] == "--version" {
		fmt.Fprintf(stdout, "%s upstream/%s\n", bridgeVersion, backendCommit)
		return 0
	}
	options, err := parseOptions(arguments)
	if err != nil {
		fmt.Fprintln(stderr, "bridgeUsageError")
		return 3
	}
	// RISK(breaking): the private report is selected only by the complete
	// reference flag pair. Omission continues through the original v1 path.
	if options.referenceRunnerBinary != "" {
		report, exitCode, err := executeReference(ctx, options)
		if err != nil {
			fmt.Fprintln(stderr, stableError(exitCode))
			return exitCode
		}
		if err := encodeReferenceReport(ctx, stdout, report); err != nil {
			fmt.Fprintln(stderr, "bridgeReportEncodeError")
			return 6
		}
		return 0
	}
	report, exitCode, err := execute(ctx, options)
	if err != nil {
		fmt.Fprintln(stderr, stableError(exitCode))
		return exitCode
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(stderr, "bridgeReportEncodeError")
		return 6
	}
	return 0
}

func parseOptions(arguments []string) (bridgeOptions, error) {
	set := flag.NewFlagSet("sentinel-mutate4go-bridge", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	projectRoot := set.String("project-root", "", "project root")
	goBinary := set.String("go-binary", "", "Go executable")
	runnerBinary := set.String("runner-binary", "", "typed Go test runner")
	var referenceRunnerBinary exactOption
	var referenceRunID exactOption
	set.Var(&referenceRunnerBinary, "reference-runner-binary", "reference execution companion")
	set.Var(&referenceRunID, "reference-run-id", "reference report run ID")
	timeoutMilliseconds := set.Int64("timeout-ms", 0, "per command timeout")
	var mutantMilliseconds int64
	set.Func("mutant-timeout-ms", "optional candidate command timeout", func(value string) error {
		return setMutantMilliseconds(&mutantMilliseconds, value)
	})
	var sources sourceList
	set.Var(&sources, "source", "production source")
	if err := set.Parse(arguments); err != nil || len(set.Args()) != 0 {
		return bridgeOptions{}, fmt.Errorf("invalid arguments")
	}
	if *timeoutMilliseconds < 1 || *timeoutMilliseconds > 86_400_000 {
		return bridgeOptions{}, fmt.Errorf("invalid timeout")
	}
	if mutantMilliseconds > *timeoutMilliseconds {
		return bridgeOptions{}, fmt.Errorf("mutant timeout exceeds command timeout")
	}
	if referenceRunnerBinary.seen != referenceRunID.seen || (referenceRunID.seen && !validLowerHex(referenceRunID.value, 32)) {
		return bridgeOptions{}, fmt.Errorf("invalid reference profile")
	}
	root, err := canonicalDirectory(*projectRoot)
	if err != nil {
		return bridgeOptions{}, err
	}
	goPath, err := canonicalExecutable(*goBinary)
	if err != nil {
		return bridgeOptions{}, err
	}
	runnerPath, err := canonicalExecutable(*runnerBinary)
	if err != nil {
		return bridgeOptions{}, err
	}
	var referenceRunnerPath string
	if referenceRunnerBinary.seen {
		referenceRunnerPath, err = canonicalExecutable(referenceRunnerBinary.value)
		if err != nil {
			return bridgeOptions{}, err
		}
	}
	validatedSources, err := validateSources(root, sources)
	if err != nil {
		return bridgeOptions{}, err
	}
	return bridgeOptions{projectRoot: root, goBinary: goPath, runnerBinary: runnerPath, referenceRunnerBinary: referenceRunnerPath, referenceRunID: referenceRunID.value, timeout: time.Duration(*timeoutMilliseconds) * time.Millisecond, mutantTimeout: time.Duration(mutantMilliseconds) * time.Millisecond, sources: validatedSources}, nil
}

type exactOption struct {
	value string
	seen  bool
}

func (option *exactOption) String() string { return option.value }

func (option *exactOption) Set(value string) error {
	if option.seen || value == "" {
		return fmt.Errorf("invalid option")
	}
	option.value = value
	option.seen = true
	return nil
}

func setMutantMilliseconds(selected *int64, value string) error {
	if *selected != 0 {
		return fmt.Errorf("duplicate mutant timeout")
	}
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds < 1 || milliseconds > 86_400_000 {
		return fmt.Errorf("invalid mutant timeout")
	}
	*selected = milliseconds
	return nil
}

func canonicalDirectory(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != filepath.Clean(absolute) {
		return "", fmt.Errorf("project root is not canonical")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("project root is not a directory")
	}
	return resolved, nil
}

func canonicalExecutable(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != filepath.Clean(absolute) {
		return "", fmt.Errorf("Go executable is not canonical")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("Go executable is invalid")
	}
	return resolved, nil
}

func validateSources(root string, sources []string) ([]string, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("source is required")
	}
	validated := append([]string(nil), sources...)
	sort.Strings(validated)
	for index, source := range validated {
		if !validRelativeSource(source) || (index > 0 && source == validated[index-1]) {
			return nil, fmt.Errorf("source is invalid")
		}
		absolute := filepath.Join(root, filepath.FromSlash(source))
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil || resolved != absolute {
			return nil, fmt.Errorf("source is not canonical")
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("source is not regular")
		}
	}
	return validated, nil
}

func validRelativeSource(source string) bool {
	return source != "" && filepath.ToSlash(filepath.Clean(source)) == source && !filepath.IsAbs(source) &&
		source != ".." && !strings.HasPrefix(source, "../") && strings.HasSuffix(source, ".go") && !strings.HasSuffix(source, "_test.go")
}

func execute(ctx context.Context, options bridgeOptions) (machineReport, int, error) {
	if err := ctx.Err(); err != nil {
		return machineReport{}, 6, err
	}
	modulePath, err := readModulePath(filepath.Join(options.projectRoot, "go.mod"))
	if err != nil {
		return machineReport{}, 5, err
	}
	profile, err := freshCoverage(ctx, options)
	if ctx.Err() != nil {
		return machineReport{}, 6, ctx.Err()
	}
	if err != nil {
		return machineReport{}, 4, err
	}
	controlPassed := runControlTwice(ctx, options)
	if ctx.Err() != nil {
		return machineReport{}, 6, ctx.Err()
	}
	if !controlPassed {
		return machineReport{}, 4, fmt.Errorf("baseline failed")
	}
	report, plan, err := discoverPlan(ctx, options, modulePath, profile)
	if err != nil {
		return machineReport{}, 6, err
	}
	outcomes, err := executePlan(ctx, options, plan)
	if err != nil {
		return machineReport{}, 6, err
	}
	if err := ctx.Err(); err != nil {
		return machineReport{}, 6, err
	}
	report.Outcomes = outcomes
	return report, 0, nil
}

func freshCoverage(ctx context.Context, options bridgeOptions) (map[string][]coverage.Segment, error) {
	coverageRoot := filepath.Join(options.projectRoot, "target", "sentinel-coverage")
	if err := os.MkdirAll(coverageRoot, 0o700); err != nil {
		return nil, err
	}
	profilePath := filepath.Join(coverageRoot, "coverage.out")
	result := runTypedTestsContext(ctx, options, profilePath)
	if result != "passed" {
		return nil, fmt.Errorf("coverage baseline failed")
	}
	profile, err := coverage.LoadProfile(profilePath)
	if err != nil || profile == nil {
		return nil, fmt.Errorf("coverage profile invalid")
	}
	return profile, nil
}

func runControlTwice(ctx context.Context, options bridgeOptions) bool {
	for index := 0; index < 2; index++ {
		if runTypedTestsContext(ctx, options, "") != "passed" {
			return false
		}
	}
	return true
}

func discoverPlan(ctx context.Context, options bridgeOptions, modulePath string, profile map[string][]coverage.Segment) (machineReport, []plannedCandidate, error) {
	report := machineReport{SchemaVersion: "sentinel-mutate4go-report-v1", BackendName: "mutate4go", BackendCommit: backendCommit}
	var plan []plannedCandidate
	for _, source := range options.sources {
		if err := ctx.Err(); err != nil {
			return machineReport{}, nil, err
		}
		inventory, candidates, err := discoverSource(options.projectRoot, modulePath, source, profile)
		if err != nil {
			return machineReport{}, nil, err
		}
		report.SourceInventory = append(report.SourceInventory, inventory)
		for _, candidate := range candidates {
			report.Candidates = append(report.Candidates, candidate.candidate)
			plan = append(plan, candidate)
		}
	}
	return report, plan, nil
}

func discoverSource(root, modulePath, source string, profile map[string][]coverage.Segment) (sourceInventory, []plannedCandidate, error) {
	absolute := filepath.Join(root, filepath.FromSlash(source))
	payload, err := os.ReadFile(absolute)
	if err != nil {
		return sourceInventory{}, nil, err
	}
	sites, _, err := mutations.Discover(absolute)
	if err != nil {
		return sourceInventory{}, nil, err
	}
	digest := sha256.Sum256(payload)
	inventory := sourceInventory{Path: source, SHA256: hex.EncodeToString(digest[:]), CandidateCount: int64(len(sites))}
	expectedProfilePath := modulePath + "/" + source
	candidates := make([]plannedCandidate, 0, len(sites))
	for _, site := range sites {
		candidate := machineCandidate{
			ID: candidateID(source, site), SourceFile: source, Line: int64(site.Line), Column: int64(site.Column), Operator: site.Description,
		}
		candidates = append(candidates, plannedCandidate{candidate: candidate, site: site, source: source, covered: exactCovered(profile[expectedProfilePath], site.Line)})
	}
	return inventory, candidates, nil
}

func candidateID(source string, site mutations.Site) string {
	payload := strings.Join([]string{
		"sentinel-mutate4go-candidate-v1", source, site.FunctionID, site.Category, site.Original, site.Mutant,
		strconv.Itoa(site.StartOffset), strconv.Itoa(site.EndOffset),
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func exactCovered(segments []coverage.Segment, line int) bool {
	for _, segment := range segments {
		if line >= segment.StartLine && line <= segment.EndLine && segment.Count > 0 {
			return true
		}
	}
	return false
}

func executePlan(ctx context.Context, options bridgeOptions, plan []plannedCandidate) ([]machineOutcome, error) {
	outcomes := make([]machineOutcome, 0, len(plan))
	for _, candidate := range plan {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		outcome, err := executeCandidate(ctx, options, candidate)
		if err != nil {
			return nil, err
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

func executeCandidate(ctx context.Context, options bridgeOptions, planned plannedCandidate) (machineOutcome, error) {
	return executeCandidateWith(ctx, options, planned, func(selected bridgeOptions) (string, error) {
		return classifyMutant(ctx, selected), nil
	})
}

func executeCandidateWith(ctx context.Context, options bridgeOptions, planned plannedCandidate, observe func(bridgeOptions) (string, error)) (machineOutcome, error) {
	if err := ctx.Err(); err != nil {
		return machineOutcome{}, err
	}
	if !planned.covered {
		return machineOutcome{CandidateID: planned.candidate.ID, Status: "uncovered", DurationNanos: 0}, nil
	}
	// RISK(side-effect): this value copy changes only candidate commands;
	// controls and the signal parent retain their existing budgets and lifetime.
	if options.mutantTimeout > 0 {
		options.timeout = options.mutantTimeout
	}
	path := filepath.Join(options.projectRoot, filepath.FromSlash(planned.source))
	original, err := os.ReadFile(path)
	if err != nil {
		return machineOutcome{}, err
	}
	mutated := mutations.Apply(string(original), planned.site)
	start := time.Now()
	status := ""
	err = withMutationRestored(ctx, path, original, []byte(mutated), func() error {
		var observeErr error
		status, observeErr = observe(options)
		return observeErr
	})
	if err != nil {
		return machineOutcome{}, err
	}
	duration := time.Since(start).Nanoseconds()
	if duration < 0 || duration > 9_007_199_254_740_991 {
		return machineOutcome{}, fmt.Errorf("duration is outside JSON safe integer range")
	}
	return machineOutcome{CandidateID: planned.candidate.ID, Status: status, DurationNanos: duration}, nil
}

func executeAndRestore(ctx context.Context, options bridgeOptions, path string, original, mutated []byte) (string, error) {
	status := ""
	err := withMutationRestored(ctx, path, original, mutated, func() error {
		status = classifyMutant(ctx, options)
		return nil
	})
	return status, err
}

// RISK(side-effect): both profiles share restoration after partial writes,
// observation failures and cancellation; restoration failure aborts the report.
func withMutationRestored(ctx context.Context, path string, original, mutated []byte, observe func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	writeErr := os.WriteFile(path, mutated, info.Mode().Perm())
	var observeErr error
	if writeErr == nil {
		observeErr = observe()
	}
	// Restoration completes even after cancellation or a partial mutation write.
	if err := errors.Join(writeErr, os.WriteFile(path, original, info.Mode().Perm())); err != nil {
		return errors.Join(err, observeErr)
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(observeErr, err)
	}
	return observeErr
}

func classifyMutant(ctx context.Context, options bridgeOptions) string {
	compile := runGo(ctx, options, "test", "./...", "-run=^$", "-count=1")
	if ctx.Err() != nil || compile == "toolError" {
		return "toolError"
	}
	if compile == "timedOut" {
		return "timedOut"
	}
	if compile != "passed" {
		return "compileError"
	}
	first := runTypedTestsContext(ctx, options, "")
	if ctx.Err() != nil {
		return "toolError"
	}
	second := runTypedTestsContext(ctx, options, "")
	if first != second {
		return "toolError"
	}
	if first == "timedOut" {
		return "timedOut"
	}
	if first == "assertionFailure" {
		return "killed"
	}
	switch first {
	case "passed":
		return "survived"
	case "compileError":
		return "compileError"
	case "runtimeError":
		return "runtimeError"
	default:
		return "toolError"
	}
}

type typedRunnerReport struct {
	SchemaVersion  string `json:"schemaVersion"`
	Nonce          string `json:"nonce"`
	Status         string `json:"status"`
	InventoryCount int    `json:"inventoryCount"`
}

func runTypedTests(options bridgeOptions, coverProfile string) string {
	return runTypedTestsContext(context.Background(), options, coverProfile)
}

func runTypedTestsContext(parent context.Context, options bridgeOptions, coverProfile string) string {
	report, err := invokeTypedTestsContext(parent, options, coverProfile)
	if err != nil {
		return "toolError"
	}
	return report.Status
}

func invokeTypedTestsContext(parent context.Context, options bridgeOptions, coverProfile string) (typedRunnerReport, error) {
	if parent.Err() != nil {
		return typedRunnerReport{}, parent.Err()
	}
	ctx, cancel := context.WithTimeout(parent, options.timeout+2*time.Second)
	defer cancel()
	arguments := []string{
		"--project-root", options.projectRoot,
		"--go-binary", options.goBinary,
		"--timeout-ms", strconv.FormatInt(options.timeout.Milliseconds(), 10),
	}
	if coverProfile != "" {
		arguments = append(arguments, "--coverprofile", coverProfile)
	}
	command := exec.CommandContext(ctx, options.runnerBinary, arguments...)
	command.Dir = options.projectRoot
	command.Env = os.Environ()
	command.Stdin = nil
	var stdout runnerOutput
	command.Stdout = &stdout
	command.Stderr = io.Discard
	runErr, cleanupErr := runCommandGroup(command, syscall.SIGTERM, 3*time.Second)
	if runErr != nil || cleanupErr != nil || ctx.Err() != nil || stdout.exceeded {
		return typedRunnerReport{}, fmt.Errorf("typed runner failed")
	}
	report, err := decodeTypedRunnerReport(stdout.buffer.Bytes())
	if err != nil {
		return typedRunnerReport{}, err
	}
	return report, nil
}

// RISK(process/cancellation): runner cancellation needs time to restore tests
// and stop its separate Go-test group. WaitDelay joins finite escalation; no
// delayed timer remains capable of signaling a reused process ID after return.
func runCommandGroup(command *exec.Cmd, signal syscall.Signal, grace time.Duration) (error, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = grace
	command.Cancel = func() error { return signalCommandGroup(command, signal) }
	runErr := command.Run()
	return runErr, signalCommandGroup(command, syscall.SIGKILL)
}

func signalCommandGroup(command *exec.Cmd, signal syscall.Signal) error {
	if command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

const typedRunnerOutputLimit = 16 * 1024 * 1024

// The named buffer deliberately exposes Write only to os/exec's copy loop.
type runnerOutput struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (output *runnerOutput) Write(payload []byte) (int, error) {
	remaining := typedRunnerOutputLimit - output.buffer.Len()
	if len(payload) > remaining {
		output.exceeded = true
		_, _ = output.buffer.Write(payload[:remaining])
		return len(payload), nil
	}
	return output.buffer.Write(payload)
}

func decodeTypedRunnerReport(payload []byte) (typedRunnerReport, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var report typedRunnerReport
	if err := decoder.Decode(&report); err != nil {
		return typedRunnerReport{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return typedRunnerReport{}, fmt.Errorf("runner report has trailing data")
	}
	if report.SchemaVersion != "sentinel-go-typed-runner-v1" || len(report.Nonce) != 32 || report.InventoryCount < 1 || !knownRunnerStatus(report.Status) {
		return typedRunnerReport{}, fmt.Errorf("runner report is invalid")
	}
	if _, err := hex.DecodeString(report.Nonce); err != nil {
		return typedRunnerReport{}, fmt.Errorf("runner nonce is invalid")
	}
	return report, nil
}

func knownRunnerStatus(status string) bool {
	switch status {
	case "passed", "assertionFailure", "runtimeError", "compileError", "timedOut", "toolError":
		return true
	default:
		return false
	}
}

func runGo(parent context.Context, options bridgeOptions, arguments ...string) string {
	result := runGoCommand(parent, options, options.timeout, arguments...)
	if parent.Err() != nil || result.cleanupErr != nil {
		return "toolError"
	}
	if result.deadlineExceeded {
		return "timedOut"
	}
	if result.runErr != nil {
		return "failed"
	}
	return "passed"
}

func runGoObservation(parent context.Context, options bridgeOptions, timeout time.Duration, arguments ...string) (compileObservation, error) {
	return referenceCompileObservation(parent, runGoCommand(parent, options, timeout, arguments...))
}

type commandResult struct {
	started          bool
	exitCode         int
	deadlineExceeded bool
	runErr           error
	cleanupErr       error
}

// Both profiles consume one supervised execution; only their error mapping differs.
func runGoCommand(parent context.Context, options bridgeOptions, timeout time.Duration, arguments ...string) commandResult {
	if parent.Err() != nil {
		return commandResult{runErr: parent.Err()}
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, options.goBinary, arguments...)
	command.Dir = options.projectRoot
	command.Env = os.Environ()
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	err, cleanupErr := runCommandGroup(command, syscall.SIGKILL, time.Second)
	exitCode := -1
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	}
	return commandResult{started: command.Process != nil, exitCode: exitCode, deadlineExceeded: ctx.Err() == context.DeadlineExceeded, runErr: err, cleanupErr: cleanupErr}
}

func referenceCompileObservation(parent context.Context, command commandResult) (compileObservation, error) {
	if parent.Err() != nil {
		return compileObservation{}, parent.Err()
	}
	if !command.started || command.cleanupErr != nil {
		return compileObservation{}, fmt.Errorf("compile execution failed")
	}
	result := compileObservation{Started: true, ExitCode: command.exitCode, CleanupSucceeded: true}
	if command.deadlineExceeded {
		result.Status = "timedOut"
		result.DeadlineExceeded = true
		return result, nil
	}
	if command.runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(command.runErr, &exitErr) {
			return compileObservation{}, fmt.Errorf("compile supervisor failed")
		}
		result.Status = "failed"
		return result, nil
	}
	result.Status = "passed"
	return result, nil
}

func readModulePath(goModPath string) (string, error) {
	payload, err := os.Open(goModPath)
	if err != nil {
		return "", err
	}
	defer payload.Close()
	scanner := bufio.NewScanner(payload)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("module path missing")
}

func stableError(exitCode int) string {
	switch exitCode {
	case 4:
		return "baselineFailed"
	case 5:
		return "dependencyError"
	default:
		return "backendError"
	}
}
