package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/gotoolchain"
	"github.com/hwain-hwang/sentinel-go/internal/history"
	"github.com/hwain-hwang/sentinel-go/internal/mutation"
	"github.com/hwain-hwang/sentinel-go/internal/orchestrator"
	"github.com/hwain-hwang/sentinel-go/internal/runid"
)

const expectedBridgeVersion = "sentinel-mutate4go-bridge/1 upstream/" + mutation.Mutate4GoCommit
const expectedRunnerVersion = "sentinel-go-test-runner/1"

var (
	lockedGoBinary      string
	lockedRunnerBinary  string
	lockedBackendSHA256 string
	lockedRunnerSHA256  string
)

const maxCompanionBytes = 16 * 1024 * 1024

var failurePriorities = map[int]int{
	ExitEvidenceError:   7,
	ExitToolError:       6,
	ExitDependencyError: 5,
	ExitBackendError:    4,
	ExitCancelled:       3,
	ExitBaselineFailed:  2,
	ExitQualityFailed:   1,
}

var terminalStatuses = map[int]string{
	ExitPassed:           "passed",
	ExitToolError:        "toolError",
	ExitQualityFailed:    "qualityFailed",
	ExitUsageConfigError: "usageConfigError",
	ExitBaselineFailed:   "baselineFailed",
	ExitDependencyError:  "dependencyError",
	ExitBackendError:     "backendError",
	ExitEvidenceError:    "evidenceError",
	ExitCancelled:        "cancelled",
}

func execute(options Options) (commandResult, int, error) {
	switch options.Command {
	case "doctor":
		return executeDoctor(options)
	case "history":
		return executeHistory(options)
	default:
		return executeQuality(options)
	}
}

func executeDoctor(options Options) (commandResult, int, error) {
	result := baseResult("doctor")
	backend, runnerBinary, goBinary, err := resolveDoctorDependencies(options)
	if err != nil || !validDoctorDependencies(backend, runnerBinary, goBinary) {
		result.Run.TerminalStatus = "dependencyError"
		return result, ExitDependencyError, fmt.Errorf("dependencyError")
	}
	result.Run.TerminalStatus = "passed"
	result.Doctor = &doctorResult{GoVersion: runtime.Version(), Backend: "mutate4go", BackendCommit: mutation.Mutate4GoCommit, Pass: true}
	return result, ExitPassed, nil
}

func resolveDoctorDependencies(options Options) (string, string, string, error) {
	backend, err := resolveBackend(options.Backend)
	if err != nil {
		return "", "", "", err
	}
	runnerBinary, err := resolveRunnerBinary()
	if err != nil {
		return "", "", "", err
	}
	goBinary, err := resolveGoBinary()
	if err != nil {
		return "", "", "", err
	}
	return backend, runnerBinary, goBinary, nil
}

func validDoctorDependencies(backend, runnerBinary, goBinary string) bool {
	if runtime.Version() != "go1.27.1" || probeGoVersion(goBinary) != "go version go1.27.1 linux/amd64" {
		return false
	}
	if !executableSHA256Matches(backend, lockedBackendSHA256) || !executableSHA256Matches(runnerBinary, lockedRunnerSHA256) {
		return false
	}
	if version, err := backendVersion(backend); err != nil || version != expectedBridgeVersion {
		return false
	}
	version, err := backendVersion(runnerBinary)
	return err == nil && version == expectedRunnerVersion
}

func executeHistory(options Options) (commandResult, int, error) {
	result := baseResult("history")
	project, err := canonicalProject(options.Project)
	if err != nil {
		result.Run.TerminalStatus = "usageConfigError"
		return result, ExitUsageConfigError, err
	}
	store := history.NewStore(project)
	runs, err := store.Load()
	if err != nil {
		result.Run.TerminalStatus = "evidenceError"
		return result, ExitEvidenceError, fmt.Errorf("evidenceError")
	}
	repeated := history.RepeatedDefects(runs)
	view := &historyResult{Runs: runs, RepeatedDefects: repeated}
	if options.Repeated {
		view.Runs = nil
		view.RepeatedDefects = onlyRepeated(repeated)
	}
	result.Run.TerminalStatus = "passed"
	result.History = view
	return result, ExitPassed, nil
}

func executeQuality(options Options) (commandResult, int, error) {
	project, err := canonicalProject(options.Project)
	if err != nil {
		result := baseResult(options.Command)
		result.Run.TerminalStatus = "usageConfigError"
		return result, ExitUsageConfigError, err
	}
	if commandIn(options.Command, "mutation", "check") {
		sources, scopeErr := orchestrator.ResolveMutationSourceInventory(project, options.Sources)
		if scopeErr != nil {
			result := baseResult(options.Command)
			result.Run.TerminalStatus = "usageConfigError"
			return result, ExitUsageConfigError, scopeErr
		}
		options.Sources = sources
	}
	store := history.NewStore(project)
	runID, startedAt, prepareExit, err := prepareQualityRun(store, options.Command)
	if err != nil {
		result := baseResult(options.Command)
		result.Run.TerminalStatus = terminalStatus(prepareExit)
		return result, prepareExit, err
	}
	result := baseResult(options.Command)
	result.Run.RunID = runID
	exitCode, runErr := runRequestedComponents(options, project, &result)
	result.Run.TerminalStatus = terminalStatus(exitCode)
	if err := recordQualityRun(store, result, startedAt, exitCode); err != nil {
		result.Run.TerminalStatus = "evidenceError"
		return result, ExitEvidenceError, fmt.Errorf("evidenceError")
	}
	return result, exitCode, runErr
}

func prepareQualityRun(store *history.Store, command string) (string, time.Time, int, error) {
	if err := store.Initialize(); err != nil {
		return "", time.Time{}, ExitEvidenceError, fmt.Errorf("evidenceError")
	}
	runID, err := newRunID()
	if err != nil {
		return "", time.Time{}, ExitToolError, fmt.Errorf("toolError")
	}
	startedAt := time.Now().UTC()
	if err := store.Start(runID, command, startedAt); err != nil {
		return "", time.Time{}, ExitEvidenceError, fmt.Errorf("evidenceError")
	}
	return runID, startedAt, ExitPassed, nil
}

func runRequestedComponents(options Options, project string, result *commandResult) (int, error) {
	exitCode := ExitPassed
	var resultError error
	if options.Command == "crap" || options.Command == "check" {
		crapReport, componentExit, err := runCrapComponent(options, project)
		result.Crap = crapReport
		exitCode, resultError = chooseFailure(exitCode, resultError, componentExit, err)
	}
	if options.Command == "mutation" || options.Command == "check" {
		mutationReport, componentExit, err := runMutationComponent(options, project)
		result.Mutation = mutationReport
		exitCode, resultError = chooseFailure(exitCode, resultError, componentExit, err)
	}
	return exitCode, resultError
}

func runCrapComponent(options Options, project string) (*orchestrator.CrapReport, int, error) {
	profile, cleanup, err := coverageProfile(options, project)
	if err != nil {
		return nil, ExitBaselineFailed, fmt.Errorf("baselineFailed")
	}
	if cleanup != nil {
		defer cleanup()
	}
	report, err := orchestrator.AnalyzeCrap(project, profile)
	if err != nil {
		return nil, ExitDependencyError, fmt.Errorf("dependencyError")
	}
	if !report.Pass {
		return &report, ExitQualityFailed, nil
	}
	return &report, ExitPassed, nil
}

func runMutationComponent(options Options, project string) (*orchestrator.MutationReport, int, error) {
	backend, goBinary, runnerBinary, err := resolveMutationExecutables(options)
	if err != nil {
		return nil, ExitDependencyError, fmt.Errorf("dependencyError")
	}
	report, err := orchestrator.RunMutation(context.Background(), orchestrator.MutationRequest{
		ProjectRoot:       project,
		Sources:           options.Sources,
		BackendExecutable: backend,
		RunnerExecutable:  runnerBinary,
		GoBinary:          goBinary,
		Timeout:           mutationBackendTimeout,
		MutantTimeout:     options.MutantTimeout,
	})
	if err != nil {
		return nil, classifyMutationError(err), err
	}
	return mutationComponentOutcome(&report)
}

func resolveMutationExecutables(options Options) (string, string, string, error) {
	backend, err := resolveBackend(options.Backend)
	if err != nil {
		return "", "", "", err
	}
	goBinary, err := resolveGoBinary()
	if err != nil {
		return "", "", "", err
	}
	runnerBinary, err := resolveRunnerBinary()
	if err != nil {
		return "", "", "", err
	}
	return backend, goBinary, runnerBinary, nil
}

func mutationComponentOutcome(report *orchestrator.MutationReport) (*orchestrator.MutationReport, int, error) {
	if report.Gate.Counts[mutation.ToolError] > 0 {
		return report, ExitBackendError, fmt.Errorf("backendError")
	}
	if !report.Gate.Pass {
		return report, ExitQualityFailed, nil
	}
	return report, ExitPassed, nil
}

func coverageProfile(options Options, project string) (string, func(), error) {
	if options.CoverProfile != "" {
		return existingCoverageProfile(options.CoverProfile, project), nil, nil
	}
	return generateCoverageProfile(project)
}

func existingCoverageProfile(path, project string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(project, path)
	}
	return filepath.Clean(path)
}

func generateCoverageProfile(project string) (string, func(), error) {
	directory, err := os.MkdirTemp("", "sentinel-go-coverage-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	profile := filepath.Join(directory, "coverage.out")
	goBinary, err := resolveGoBinary()
	if err != nil {
		cleanup()
		return "", nil, err
	}
	environment, err := gotoolchain.OfflineEnvironment(goBinary)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	command := exec.Command(goBinary, "test", "./...", "-count=1", "-coverprofile="+profile)
	command.Dir = project
	command.Env = environment
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	if err := command.Run(); err != nil {
		cleanup()
		return "", nil, err
	}
	return profile, cleanup, nil
}

func recordQualityRun(store *history.Store, result commandResult, startedAt time.Time, exitCode int) error {
	findings, err := findingsForResult(store, result)
	if err != nil {
		return err
	}
	components, err := qualityComponents(result.Run.Command, result)
	if err != nil {
		return err
	}
	completedAt := time.Now().UTC()
	record := history.RunRecord{
		Certification:      exitCode == ExitPassed && qualityComponentsPass(components),
		Command:            result.Run.Command,
		CommittedAtUTC:     completedAt,
		CompletedAtUTC:     completedAt,
		Components:         components,
		CorrelationID:      result.Run.RunID,
		DiagnosticCodes:    diagnosticCodesForResult(result),
		ExitCode:           exitCode,
		FingerprintVersion: "sentinel-fingerprint-v1",
		Language:           "go",
		Mode:               "strict",
		ObservationSource:  "fresh",
		OccurredAtUTC:      startedAt,
		RunID:              result.Run.RunID,
		SchemaVersion:      history.RunSchemaVersion,
		SpecVersion:        "1.0.0",
		TerminalStatus:     result.Run.TerminalStatus,
		Findings:           findings,
	}
	return store.Append(record)
}

func qualityComponents(command string, result commandResult) (history.Components, error) {
	var components history.Components
	if command == "crap" || command == "check" {
		component, err := crapEvidenceComponent(result.Crap)
		if err != nil {
			return history.Components{}, err
		}
		components.Crap = &component
	}
	if command == "mutation" || command == "check" {
		component, err := mutationEvidenceComponent(result.Mutation)
		if err != nil {
			return history.Components{}, err
		}
		components.Mutation = &component
	}
	return components, nil
}

func crapEvidenceComponent(report *orchestrator.CrapReport) (history.CrapComponent, error) {
	component := history.CrapComponent{MaxNumerator: "0", MaxDenominator: "1"}
	if report == nil {
		return component, nil
	}
	component, maximum, err := summarizeCrapRows(report.Rows)
	if err != nil {
		return history.CrapComponent{}, err
	}
	if maximum.known {
		component.MaxNumerator = maximum.numerator.String()
		component.MaxDenominator = maximum.denominator.String()
	}
	component.Pass = crapComponentPass(component, maximum)
	if component.Pass != report.Pass {
		return history.CrapComponent{}, fmt.Errorf("CRAP report pass disagrees with exact component")
	}
	return component, nil
}

type exactMaximum struct {
	numerator   *big.Int
	denominator *big.Int
	known       bool
}

func summarizeCrapRows(rows []orchestrator.CrapResultRow) (history.CrapComponent, exactMaximum, error) {
	component := history.CrapComponent{CallableCount: uint64(len(rows)), MaxNumerator: "0", MaxDenominator: "1"}
	maximum := exactMaximum{numerator: big.NewInt(0), denominator: big.NewInt(1)}
	for _, row := range rows {
		if row.UnknownReason != "" {
			component.UnknownCount++
			continue
		}
		numerator, denominator, err := parseCrapFraction(row.Numerator, row.Denominator)
		if err != nil {
			return history.CrapComponent{}, exactMaximum{}, err
		}
		maximum.observe(numerator, denominator)
	}
	return component, maximum, nil
}

func (maximum *exactMaximum) observe(numerator, denominator *big.Int) {
	if maximum.known && !fractionGreater(numerator, denominator, maximum.numerator, maximum.denominator) {
		return
	}
	maximum.numerator.Set(numerator)
	maximum.denominator.Set(denominator)
	maximum.known = true
}

func crapComponentPass(component history.CrapComponent, maximum exactMaximum) bool {
	limit := new(big.Int).Mul(big.NewInt(8), maximum.denominator)
	return component.CallableCount > 0 && component.UnknownCount == 0 && maximum.numerator.Cmp(limit) <= 0
}

func parseCrapFraction(numeratorText, denominatorText string) (*big.Int, *big.Int, error) {
	numerator, err := parseCrapInteger(numeratorText, true)
	if err != nil {
		return nil, nil, err
	}
	denominator, err := parseCrapInteger(denominatorText, false)
	if err != nil {
		return nil, nil, err
	}
	if new(big.Int).GCD(nil, nil, numerator, denominator).Cmp(big.NewInt(1)) != 0 {
		return nil, nil, fmt.Errorf("unreduced CRAP fraction")
	}
	return numerator, denominator, nil
}

func parseCrapInteger(text string, allowZero bool) (*big.Int, error) {
	value, ok := new(big.Int).SetString(text, 10)
	if !ok || value.String() != text {
		return nil, fmt.Errorf("invalid CRAP fraction")
	}
	if value.Sign() < 0 || (!allowZero && value.Sign() == 0) {
		return nil, fmt.Errorf("invalid CRAP fraction")
	}
	return value, nil
}

func fractionGreater(leftNumerator, leftDenominator, rightNumerator, rightDenominator *big.Int) bool {
	left := new(big.Int).Mul(leftNumerator, rightDenominator)
	right := new(big.Int).Mul(rightNumerator, leftDenominator)
	return left.Cmp(right) > 0
}

func mutationEvidenceComponent(report *orchestrator.MutationReport) (history.MutationComponent, error) {
	if report == nil {
		return history.MutationComponent{}, nil
	}
	rawCounts := make(map[string]int64, len(report.Gate.Counts))
	for status, count := range report.Gate.Counts {
		rawCounts[string(status)] = count
	}
	verified, err := mutation.EvaluateCounts(rawCounts, report.Gate.InScope, report.Gate.UnauthorizedExclusion)
	if err != nil || verified.Pass != report.Gate.Pass {
		return history.MutationComponent{}, fmt.Errorf("invalid mutation gate component")
	}
	return history.MutationComponent{
		CompileError:          uint64(verified.Counts[mutation.CompileError]),
		Ignored:               uint64(verified.Counts[mutation.Ignored]),
		InScope:               uint64(verified.InScope),
		Killed:                uint64(verified.Counts[mutation.Killed]),
		Pass:                  verified.Pass,
		Pending:               uint64(verified.Counts[mutation.Pending]),
		RuntimeError:          uint64(verified.Counts[mutation.RuntimeError]),
		Survived:              uint64(verified.Counts[mutation.Survived]),
		TimedOut:              uint64(verified.Counts[mutation.TimedOut]),
		ToolError:             uint64(verified.Counts[mutation.ToolError]),
		UnauthorizedExclusion: uint64(verified.UnauthorizedExclusion),
		Uncovered:             uint64(verified.Counts[mutation.Uncovered]),
	}, nil
}

func qualityComponentsPass(components history.Components) bool {
	passes := make([]bool, 0, 2)
	if components.Crap != nil {
		passes = append(passes, components.Crap.Pass)
	}
	if components.Mutation != nil {
		passes = append(passes, components.Mutation.Pass)
	}
	for _, pass := range passes {
		if !pass {
			return false
		}
	}
	return len(passes) > 0
}

func diagnosticCodesForResult(result commandResult) []string {
	codes := make(map[string]struct{})
	addCrapDiagnosticCodes(codes, result.Crap)
	addMutationDiagnosticCodes(codes, result.Mutation)
	ordered := make([]string, 0, len(codes))
	for code := range codes {
		ordered = append(ordered, code)
	}
	sort.Strings(ordered)
	return ordered
}

func addCrapDiagnosticCodes(codes map[string]struct{}, report *orchestrator.CrapReport) {
	if report == nil {
		return
	}
	for _, row := range report.Rows {
		if row.UnknownReason != "" {
			codes[row.UnknownReason] = struct{}{}
			continue
		}
		if !row.Pass {
			codes["crapAboveLimit"] = struct{}{}
		}
	}
}

func addMutationDiagnosticCodes(codes map[string]struct{}, report *orchestrator.MutationReport) {
	if report == nil {
		return
	}
	for _, mutant := range report.Mutants {
		if mutant.Status != mutation.Killed {
			codes[string(mutant.Status)+"Mutant"] = struct{}{}
		}
	}
}

func findingsForResult(store *history.Store, result commandResult) ([]history.Finding, error) {
	findings, err := crapFindings(store, result.Crap)
	if err != nil {
		return nil, err
	}
	mutationRows, err := mutationFindings(store, result.Mutation)
	if err != nil {
		return nil, err
	}
	return append(findings, mutationRows...), nil
}

func crapFindings(store *history.Store, report *orchestrator.CrapReport) ([]history.Finding, error) {
	if report == nil {
		return nil, nil
	}
	findings := make([]history.Finding, 0)
	for _, row := range report.Rows {
		kind := crapFindingKind(row)
		if kind == "" {
			continue
		}
		finding, err := projectFinding(store, "crap", kind, row.CallableID)
		if err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

func mutationFindings(store *history.Store, report *orchestrator.MutationReport) ([]history.Finding, error) {
	if report == nil {
		return nil, nil
	}
	findings := make([]history.Finding, 0)
	for _, mutant := range report.Mutants {
		if mutant.Status == mutation.Killed {
			continue
		}
		finding, err := projectFinding(store, "mutation", string(mutant.Status), mutant.CandidateID)
		if err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

func projectFinding(store *history.Store, component, kind, identity string) (history.Finding, error) {
	fingerprint, err := store.Fingerprint(component, kind, identity)
	if err != nil {
		return history.Finding{}, err
	}
	return history.Finding{Fingerprint: fingerprint, Component: component, Kind: kind}, nil
}

func crapFindingKind(row orchestrator.CrapResultRow) string {
	if row.UnknownReason != "" {
		return row.UnknownReason
	}
	if !row.Pass {
		return "aboveLimit"
	}
	return ""
}

func baseResult(command string) commandResult {
	return commandResult{SchemaVersion: "sentinel-go-result-v1", Run: runResult{Command: command}}
}

func canonicalProject(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != filepath.Clean(absolute) {
		return "", fmt.Errorf("project path is not canonical")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("project path is not a directory")
	}
	return resolved, nil
}

func resolveBackend(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return companionPath(executable, "sentinel-mutate4go-bridge"), nil
}

func resolveRunnerBinary() (string, error) {
	if lockedRunnerBinary != "" {
		return filepath.Clean(lockedRunnerBinary), nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return companionPath(executable, "sentinel-go-test-runner"), nil
}

func companionPath(executable, name string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(executable)), "libexec", name)
}

func executableSHA256Matches(path, expected string) bool {
	if expected == "" {
		return true
	}
	if len(expected) != sha256.Size*2 {
		return false
	}
	for _, character := range expected {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(fd), "companion")
	if file == nil {
		syscall.Close(fd)
		return false
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o111 == 0 || before.Mode().Perm()&0o022 != 0 || before.Size() <= 0 || before.Size() > maxCompanionBytes {
		return false
	}
	metadata, ok := before.Sys().(*syscall.Stat_t)
	if !ok || metadata.Nlink != 1 || metadata.Uid != uint32(os.Geteuid()) {
		return false
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxCompanionBytes+1))
	if err != nil || written != before.Size() {
		return false
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || after.Mode() != before.Mode() {
		return false
	}
	return fmt.Sprintf("%x", hash.Sum(nil)) == expected
}

func resolveGoBinary() (string, error) {
	if lockedGoBinary != "" {
		return filepath.Clean(lockedGoBinary), nil
	}
	if environmentRoot := os.Getenv("GOROOT"); environmentRoot != "" {
		return filepath.Join(environmentRoot, "bin", "go"), nil
	}
	return "", fmt.Errorf("pinned Go executable is not configured")
}

func probeGoVersion(goBinary string) string {
	command := exec.Command(goBinary, "version")
	command.Env = []string{"LC_ALL=C", "TZ=UTC", "GOROOT=" + filepath.Dir(filepath.Dir(goBinary)), "GOTOOLCHAIN=local"}
	payload, err := command.Output()
	if err != nil || len(payload) > 256 {
		return ""
	}
	return strings.TrimSpace(string(payload))
}

func backendVersion(backend string) (string, error) {
	command := exec.Command(backend, "--version")
	command.Env = []string{"LC_ALL=C", "TZ=UTC"}
	command.Stdin = nil
	payload, err := command.Output()
	if err != nil || len(payload) > 512 {
		return "", fmt.Errorf("backend version probe failed")
	}
	return strings.TrimSpace(string(payload)), nil
}

func classifyMutationError(err error) int {
	var adapterError *mutation.AdapterError
	if errors.As(err, &adapterError) {
		return adapterError.ExitCode
	}
	return ExitBackendError
}

func chooseFailure(current int, currentError error, candidate int, candidateError error) (int, error) {
	if failurePriority(candidate) > failurePriority(current) {
		return candidate, candidateError
	}
	return current, currentError
}

func failurePriority(exitCode int) int {
	return failurePriorities[exitCode]
}

func terminalStatus(exitCode int) string {
	status, exists := terminalStatuses[exitCode]
	if !exists {
		return "toolError"
	}
	return status
}

func newRunID() (string, error) {
	return runid.New()
}

func onlyRepeated(defects []history.RepeatedDefect) []history.RepeatedDefect {
	result := make([]history.RepeatedDefect, 0)
	for _, defect := range defects {
		if defect.Repeated {
			result = append(result, defect)
		}
	}
	return result
}
