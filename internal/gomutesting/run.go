package gomutesting

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/gotoolchain"
	"github.com/hwain-hwang/sentinel-go/internal/mutation"
	"github.com/hwain-hwang/sentinel-go/internal/runner"
	"github.com/hwain-hwang/sentinel-go/internal/workspace"
)

// Request has explicit source scope and a shared execution deadline, not shell input.
// Synchronous snapshot I/O, discovery and cleanup cannot be interrupted mid-operation.
type Request struct {
	ProjectRoot string
	Sources     []string
	GoBinary    string
	Timeout     time.Duration
	// RISK(breaking): zero preserves the legacy shared deadline; a positive
	// value limits each candidate replay and must never shorten controls.
	MutantTimeout time.Duration
}

// Report is an experimental observation only. It cannot produce certified evidence.
type Report struct {
	Backend       string                     `json:"backend"`
	Version       string                     `json:"version"`
	Profile       string                     `json:"profile"`
	Certified     bool                       `json:"certified"`
	Sources       []mutation.SourceInventory `json:"sources"`
	Candidates    []Candidate                `json:"candidates"`
	Records       []mutation.MutantRecord    `json:"records"`
	Counts        map[mutation.Status]int    `json:"counts"`
	ProjectSHA256 string                     `json:"projectSha256"`
	PlanSHA256    string                     `json:"planSha256"`
	Control       [2]ExecutionIdentity       `json:"control"`
	Replays       []CandidateReplay          `json:"replays"`
}

// Run uses external operators and the existing typed test runner in disposable copies.
// RISK(security): this limited profile is deliberately excluded from strict evidence;
// this remains an observation, not an admitted strict evidence producer.
func Run(parent context.Context, request Request) (returnedReport Report, returnedError error) {
	if err := validateRequest(request); err != nil {
		return Report{}, err
	}
	ctx, cancel := context.WithTimeout(parent, request.Timeout)
	defer cancel()
	if err := checkContext(ctx); err != nil {
		return Report{}, err
	}
	template, err := workspace.Create(request.ProjectRoot)
	if err != nil {
		return Report{}, fmt.Errorf("goMutestingSnapshotFailed")
	}
	// The snapshot finalizer runs first. This bounded public boundary then keeps
	// cleanup errors authoritative and discards a report on late cancellation.
	defer finalizeRun(ctx, &returnedReport, &returnedError)
	defer finishSnapshot(template, &returnedError)
	report, err := discoverRequested(template.Root, request.Sources)
	if err != nil {
		return Report{}, err
	}
	report.ProjectSHA256 = template.ContentSHA256()
	report.PlanSHA256 = planDigest(report)
	if err := executePlan(ctx, template.Root, request, &report); err != nil {
		return Report{}, err
	}
	return report, nil
}

func validateRequest(request Request) error {
	if !validRequestTimeouts(request) || len(request.Sources) == 0 {
		return fmt.Errorf("goMutestingRequestInvalid")
	}
	seen := map[string]bool{}
	for _, source := range request.Sources {
		if !validSourcePath(source) || seen[source] {
			return fmt.Errorf("goMutestingSourceInvalid")
		}
		seen[source] = true
	}
	if _, err := gotoolchain.OfflineEnvironment(request.GoBinary); err != nil {
		return fmt.Errorf("goMutestingDependencyInvalid")
	}
	return nil
}

func validRequestTimeouts(request Request) bool {
	return request.Timeout > 0 && request.Timeout <= time.Hour && request.MutantTimeout >= 0 &&
		request.MutantTimeout <= time.Hour && request.MutantTimeout <= request.Timeout
}

func newReport() Report {
	counts := map[mutation.Status]int{}
	for _, status := range []mutation.Status{mutation.Killed, mutation.Survived, mutation.Uncovered, mutation.TimedOut, mutation.CompileError, mutation.RuntimeError, mutation.Pending, mutation.Ignored, mutation.ToolError} {
		counts[status] = 0
	}
	return Report{Backend: "avito-tech/go-mutesting", Version: Version, Profile: Profile, Certified: false,
		Sources: []mutation.SourceInventory{}, Candidates: []Candidate{}, Records: []mutation.MutantRecord{}, Counts: counts,
		Replays: []CandidateReplay{}}
}

func discoverRequested(root string, sources []string) (Report, error) {
	report := newReport()
	ordered := append([]string(nil), sources...)
	sort.Strings(ordered)
	total := 0
	for _, source := range ordered {
		payload, err := readSourceFile(root, source)
		if err != nil {
			return Report{}, err
		}
		sites, err := Discover(source, payload)
		if err != nil {
			return Report{}, err
		}
		for _, site := range sites {
			total += len(site.replacement)
		}
		if total > maximumPlanBytes {
			return Report{}, fmt.Errorf("goMutestingPlanTooLarge")
		}
		report.Sources = append(report.Sources, mutation.SourceInventory{Path: source, SHA256: digest(payload), CandidateCount: int64(len(sites))})
		report.Candidates = append(report.Candidates, sites...)
	}
	return report, nil
}

func readSourceFile(root, source string) ([]byte, error) {
	file, err := os.Open(filepath.Join(root, filepath.FromSlash(source)))
	if err != nil {
		return nil, fmt.Errorf("goMutestingSourceMissing")
	}
	defer file.Close()
	return boundedSource(file)
}

func boundedSource(input io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(input, maximumSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("goMutestingSourceReadFailed")
	}
	if len(payload) > maximumSourceBytes {
		return nil, fmt.Errorf("goMutestingSourceTooLarge")
	}
	return payload, nil
}

func executePlan(ctx context.Context, root string, request Request, report *Report) error {
	control, err := executeControls(ctx, root, request, report)
	if err != nil {
		return err
	}
	for _, candidate := range report.Candidates {
		a, b, err := replay(ctx, root, request, &candidate)
		if err != nil {
			return err
		}
		state := classify(a, b)
		if !sameTestInventory(control, a) || !sameTestInventory(control, b) {
			state = mutation.ToolError
		}
		report.Records = append(report.Records, mutation.MutantRecord{CandidateID: candidate.ID, Status: state})
		report.Counts[state]++
		report.Replays = append(report.Replays, CandidateReplay{CandidateID: candidate.ID,
			First: executionIdentity(a), Second: executionIdentity(b)})
	}
	return checkContext(ctx)
}

func executeControls(ctx context.Context, root string, request Request, report *Report) (runner.Report, error) {
	first, second, err := replay(ctx, root, request, nil)
	if err != nil {
		return runner.Report{}, err
	}
	if classify(first, second) != mutation.Survived {
		return runner.Report{}, fmt.Errorf("goMutestingBaselineFailed")
	}
	if first.InputSHA256 != report.ProjectSHA256 {
		return runner.Report{}, fmt.Errorf("goMutestingControlSourceMismatch")
	}
	report.Control = [2]ExecutionIdentity{executionIdentity(first), executionIdentity(second)}
	return first, nil
}

func replay(ctx context.Context, root string, request Request, candidate *Candidate) (runner.Report, runner.Report, error) {
	first, err := runFresh(ctx, root, request, candidate)
	if err != nil {
		return runner.Report{}, runner.Report{}, err
	}
	second, err := runFresh(ctx, root, request, candidate)
	return first, second, err
}

func runFresh(ctx context.Context, root string, request Request, candidate *Candidate) (_ runner.Report, returnedError error) {
	if err := checkContext(ctx); err != nil {
		return runner.Report{}, err
	}
	snapshot, err := workspace.Create(root)
	if err != nil {
		return runner.Report{}, fmt.Errorf("goMutestingSnapshotFailed")
	}
	defer finishSnapshot(snapshot, &returnedError)
	if candidate != nil {
		if err := os.WriteFile(filepath.Join(snapshot.Root, filepath.FromSlash(candidate.SourceFile)), candidate.replacement, 0o600); err != nil {
			return runner.Report{}, fmt.Errorf("goMutestingApplyFailed")
		}
	}
	timeout := request.Timeout
	if candidate != nil && request.MutantTimeout > 0 {
		timeout = request.MutantTimeout
	}
	return runGuarded(ctx, snapshot.Root, request, timeout)
}

func runGuarded(ctx context.Context, root string, request Request, timeout time.Duration) (_ runner.Report, returnedError error) {
	// Reuse the established manifest guard to detect tests changing source or test files.
	guard, err := workspace.Create(root)
	if err != nil {
		return runner.Report{}, fmt.Errorf("goMutestingSnapshotFailed")
	}
	defer finishSnapshot(guard, &returnedError)
	result, err := runner.Execute(ctx, runner.Request{ProjectRoot: root, GoBinary: request.GoBinary, Timeout: timeout, CaptureReplay: true})
	if contextErr := checkContext(ctx); contextErr != nil {
		return runner.Report{}, contextErr
	}
	if err != nil {
		return runner.Report{}, fmt.Errorf("goMutestingRunnerFailed")
	}
	result.InputSHA256 = guard.ContentSHA256()
	return result, nil
}

func finishSnapshot(snapshot *workspace.Snapshot, returnedError *error) {
	if err := snapshot.VerifyOriginal(); err != nil {
		*returnedError = fmt.Errorf("goMutestingSourceChanged")
	}
	if err := snapshot.Remove(); err != nil {
		*returnedError = fmt.Errorf("goMutestingCleanupFailed")
	}
}

func finalizeRun(ctx context.Context, returnedReport *Report, returnedError *error) {
	if *returnedError == nil {
		*returnedError = checkContext(ctx)
	}
	if *returnedError != nil {
		*returnedReport = Report{}
	}
}

func checkContext(ctx context.Context) error {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("goMutestingTimedOut")
	}
	if ctx.Err() != nil {
		return fmt.Errorf("goMutestingCancelled")
	}
	return nil
}

func validExecution(report runner.Report) bool {
	return report.SchemaVersion == runner.RunnerSchema && validHex(report.Nonce, 16) &&
		report.InventoryCount > 0 && validHex(report.InputSHA256, 32) && validReplayIdentity(report)
}

func classify(first, second runner.Report) mutation.Status {
	if !consistentReplays(first, second) {
		return mutation.ToolError
	}
	return normalizedStatus(first.Status)
}

func consistentReplays(first, second runner.Report) bool {
	if !validExecution(first) || !validExecution(second) || first.Nonce == second.Nonce {
		return false
	}
	return first.InventoryCount == second.InventoryCount && first.Status == second.Status &&
		first.InputSHA256 == second.InputSHA256 && *first.Replay == *second.Replay
}

func validHex(value string, length int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == length && strings.ToLower(value) == value
}

func validReplayIdentity(report runner.Report) bool {
	if report.Replay == nil || !validHex(report.Replay.InventorySHA256, 32) || !validHex(report.Replay.ResultsSHA256, 32) {
		return false
	}
	if report.Status == runner.StatusAssertionFailure {
		return validHex(report.Replay.FailureSHA256, 32)
	}
	return report.Replay.FailureSHA256 == ""
}

func sameTestInventory(first, second runner.Report) bool {
	return first.Replay != nil && second.Replay != nil && first.InventoryCount == second.InventoryCount &&
		first.Replay.InventorySHA256 == second.Replay.InventorySHA256
}

func normalizedStatus(status runner.Status) mutation.Status {
	switch status {
	case runner.StatusPassed:
		return mutation.Survived
	case runner.StatusAssertionFailure:
		return mutation.Killed
	case runner.StatusRuntimeError:
		return mutation.RuntimeError
	case runner.StatusCompileError:
		return mutation.CompileError
	case runner.StatusTimedOut:
		return mutation.TimedOut
	default:
		return mutation.ToolError
	}
}
