package mutation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/gotoolchain"
	"github.com/hwain-hwang/sentinel-go/internal/workspace"
)

const bridgeOutputLimit = 16 * 1024 * 1024

// Request contains the explicit, closed inputs accepted by the mutate4go
// process adapter.
type Request struct {
	ProjectRoot       string
	Sources           []string
	BackendExecutable string
	RunnerExecutable  string
	GoBinary          string
	Timeout           time.Duration
	MutantTimeout     time.Duration
}

// AdapterError carries a stable classification without exposing raw child
// output, source paths, or environment values.
type AdapterError struct {
	Code     string
	ExitCode int
}

func (err *AdapterError) Error() string {
	return err.Code
}

type Adapter struct{}

func NewAdapter() *Adapter {
	return &Adapter{}
}

// Run executes the bridge in a disposable snapshot, validates the complete
// machine report, and proves the original project did not change.
func (adapter *Adapter) Run(ctx context.Context, request Request) (BridgeReport, error) {
	validated, err := validateRequest(request)
	if err != nil {
		return BridgeReport{}, err
	}
	snapshot, err := workspace.Create(validated.ProjectRoot)
	if err != nil {
		return BridgeReport{}, &AdapterError{Code: "snapshotCreateFailed", ExitCode: 5}
	}
	report, err := runAndVerifyOriginal(ctx, snapshot, validated)
	if err == nil {
		report, err = validateBridgeResult(snapshot.Root, validated.Sources, report)
	}
	return finishAdapter(ctx, report, err, snapshot.Remove())
}

func finishAdapter(ctx context.Context, report BridgeReport, err, cleanupErr error) (BridgeReport, error) {
	if cleanupErr != nil {
		// RISK(security): expose removal classification, never the filesystem
		// error or path, while retaining the original public error and exit code.
		removalErr := &AdapterError{Code: "snapshotRemoveFailed", ExitCode: 5}
		if err == nil {
			err = removalErr
		} else {
			err = &adapterCleanupError{primary: err, cleanup: removalErr}
		}
	}
	if err == nil && ctx.Err() != nil {
		err = &AdapterError{Code: "backendProcessFailed", ExitCode: 6}
	}
	if err != nil {
		return BridgeReport{}, err
	}
	return report, nil
}

type adapterCleanupError struct {
	primary error
	cleanup *AdapterError
}

func (err *adapterCleanupError) Error() string {
	return err.primary.Error()
}

func (err *adapterCleanupError) Unwrap() []error {
	return []error{err.primary, err.cleanup}
}

func runAndVerifyOriginal(ctx context.Context, snapshot *workspace.Snapshot, request Request) (BridgeReport, error) {
	report, runErr := runBridge(ctx, snapshot.Root, request)
	if immutableErr := snapshot.VerifyOriginal(); immutableErr != nil {
		return BridgeReport{}, &AdapterError{Code: "protectedSourceChanged", ExitCode: 1}
	}
	if runErr != nil {
		return BridgeReport{}, runErr
	}
	return report, nil
}

func validateBridgeResult(snapshotRoot string, sources []string, report BridgeReport) (BridgeReport, error) {
	if err := validateRequestedInventory(snapshotRoot, sources, report.SourceInventory); err != nil {
		return BridgeReport{}, err
	}
	if _, err := Normalize(report); err != nil {
		return BridgeReport{}, &AdapterError{Code: err.Error(), ExitCode: 6}
	}
	return report, nil
}

func validateRequest(request Request) (Request, error) {
	if !validBackendTimeout(request.Timeout) {
		return Request{}, &AdapterError{Code: "backendTimeoutInvalid", ExitCode: 3}
	}
	if !validMutantTimeout(request.MutantTimeout, request.Timeout) {
		return Request{}, &AdapterError{Code: "mutantTimeoutInvalid", ExitCode: 3}
	}
	backend, runnerBinary, goBinary, err := trustedRequestExecutables(request)
	if err != nil {
		return Request{}, err
	}
	sources, err := validatedSources(request.Sources)
	if err != nil {
		return Request{}, err
	}
	request.BackendExecutable = backend
	request.RunnerExecutable = runnerBinary
	request.GoBinary = goBinary
	request.Sources = sources
	return request, nil
}

func validBackendTimeout(timeout time.Duration) bool {
	return timeout > 0 && timeout <= 24*time.Hour
}

func validMutantTimeout(timeout, outer time.Duration) bool {
	return timeout >= 0 && timeout <= outer && timeout <= 24*time.Hour && timeout%time.Millisecond == 0
}

func trustedRequestExecutables(request Request) (string, string, string, error) {
	backend, err := trustedExecutable(request.BackendExecutable)
	if err != nil {
		return "", "", "", &AdapterError{Code: "backendExecutableInvalid", ExitCode: 5}
	}
	runnerBinary, err := trustedExecutable(request.RunnerExecutable)
	if err != nil {
		return "", "", "", &AdapterError{Code: "runnerExecutableInvalid", ExitCode: 5}
	}
	goBinary, err := trustedExecutable(request.GoBinary)
	if err != nil {
		return "", "", "", &AdapterError{Code: "goExecutableInvalid", ExitCode: 5}
	}
	return backend, runnerBinary, goBinary, nil
}

func validatedSources(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, &AdapterError{Code: "mutationSourceMissing", ExitCode: 3}
	}
	sources := append([]string(nil), requested...)
	sort.Strings(sources)
	for index, source := range sources {
		if !canonicalProductionPath(source) || duplicateSource(sources, index) {
			return nil, &AdapterError{Code: "mutationSourceInvalid", ExitCode: 3}
		}
	}
	return sources, nil
}

func duplicateSource(sources []string, index int) bool {
	return index > 0 && sources[index] == sources[index-1]
}

func trustedExecutable(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("executable path is empty")
	}
	canonical, err := canonicalExecutablePath(value)
	if err != nil {
		return "", err
	}
	if err := validateExecutableFile(canonical); err != nil {
		return "", err
	}
	return canonical, nil
}

func canonicalExecutablePath(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil || canonical != filepath.Clean(absolute) {
		return "", fmt.Errorf("executable path is not canonical")
	}
	return canonical, nil
}

func validateExecutableFile(canonical string) error {
	info, err := os.Lstat(canonical)
	if err != nil {
		return fmt.Errorf("inspect executable: %w", err)
	}
	if !trustedExecutableMode(info.Mode()) {
		return fmt.Errorf("executable is not a trusted regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("executable owner is not trusted")
	}
	return nil
}

func trustedExecutableMode(mode os.FileMode) bool {
	return mode.IsRegular() && mode.Perm()&0o111 != 0 && mode.Perm()&0o022 == 0
}

func runBridge(parent context.Context, snapshotRoot string, request Request) (BridgeReport, error) {
	environment, err := gotoolchain.OfflineEnvironment(request.GoBinary)
	if err != nil {
		return BridgeReport{}, &AdapterError{Code: "goEnvironmentInvalid", ExitCode: 5}
	}
	ctx, cancel := context.WithTimeout(parent, request.Timeout)
	defer cancel()
	arguments := bridgeArguments(request)
	command := exec.CommandContext(ctx, request.BackendExecutable, arguments...)
	command.Dir = snapshotRoot
	command.Env = environment
	command.Stdin = nil
	var stdout, stderr limitedBuffer
	stdout.limit = bridgeOutputLimit
	stderr.limit = bridgeOutputLimit
	command.Stdout = struct{ io.Writer }{&stdout}
	command.Stderr = struct{ io.Writer }{&stderr}
	runErr, cleanupErr := runBridgeGroup(command)
	if stdout.exceeded || stderr.exceeded {
		return BridgeReport{}, &AdapterError{Code: "backendOutputTooLarge", ExitCode: 6}
	}
	if cleanupErr != nil {
		return BridgeReport{}, &AdapterError{Code: "backendProcessFailed", ExitCode: 6}
	}
	if runErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return BridgeReport{}, &AdapterError{Code: "backendTimedOut", ExitCode: 6}
		}
		return BridgeReport{}, &AdapterError{Code: "backendProcessFailed", ExitCode: bridgeFailureCode(runErr)}
	}
	if ctx.Err() != nil {
		return BridgeReport{}, &AdapterError{Code: "backendProcessFailed", ExitCode: 6}
	}
	report, err := decodeBridgeReport(stdout.Bytes())
	if err != nil {
		return BridgeReport{}, &AdapterError{Code: "backendReportInvalid", ExitCode: 6}
	}
	return report, nil
}

// RISK(process/cancellation): allow bridge restoration and its separately
// owned runner groups to stop before finite escalation and pipe closure.
func runBridgeGroup(command *exec.Cmd) (error, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 5 * time.Second
	command.Cancel = func() error { return signalBridgeGroup(command, syscall.SIGTERM) }
	runErr := command.Run()
	return runErr, signalBridgeGroup(command, syscall.SIGKILL)
}

func signalBridgeGroup(command *exec.Cmd, signal syscall.Signal) error {
	if command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func bridgeArguments(request Request) []string {
	arguments := []string{
		"--project-root", ".",
		"--go-binary", request.GoBinary,
		"--runner-binary", request.RunnerExecutable,
		"--timeout-ms", strconv.FormatInt(request.Timeout.Milliseconds(), 10),
	}
	for _, source := range request.Sources {
		arguments = append(arguments, "--source", source)
	}
	// RISK(breaking): omit the new flag for legacy pinned bridge artifacts.
	if request.MutantTimeout > 0 {
		arguments = append(arguments, "--mutant-timeout-ms", strconv.FormatInt(request.MutantTimeout.Milliseconds(), 10))
	}
	return arguments
}

func decodeBridgeReport(payload []byte) (BridgeReport, error) {
	if _, err := decodeStrictJSON(payload); err != nil {
		return BridgeReport{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var report BridgeReport
	if err := decoder.Decode(&report); err != nil {
		return BridgeReport{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return BridgeReport{}, fmt.Errorf("bridge report has trailing data")
	}
	return report, nil
}

func validateRequestedInventory(snapshotRoot string, sources []string, inventory []SourceInventory) error {
	if len(sources) != len(inventory) {
		return &AdapterError{Code: "backendSourceInventoryMismatch", ExitCode: 6}
	}
	byPath := make(map[string]SourceInventory, len(inventory))
	for _, entry := range inventory {
		byPath[entry.Path] = entry
	}
	for _, source := range sources {
		entry, exists := byPath[source]
		if !exists {
			return &AdapterError{Code: "backendSourceInventoryMismatch", ExitCode: 6}
		}
		payload, err := os.ReadFile(filepath.Join(snapshotRoot, filepath.FromSlash(source)))
		if err != nil {
			return &AdapterError{Code: "backendSourceReadFailed", ExitCode: 6}
		}
		digest := sha256.Sum256(payload)
		if hex.EncodeToString(digest[:]) != entry.SHA256 {
			return &AdapterError{Code: "backendSourceDigestMismatch", ExitCode: 6}
		}
	}
	return nil
}

func bridgeFailureCode(err error) int {
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		return 6
	}
	code := exitError.ExitCode()
	if code >= 4 && code <= 6 {
		return code
	}
	return 6
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *limitedBuffer) Write(payload []byte) (int, error) {
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 {
		buffer.exceeded = true
		return len(payload), nil
	}
	if len(payload) > remaining {
		buffer.exceeded = true
		_, _ = buffer.Buffer.Write(payload[:remaining])
		return len(payload), nil
	}
	return buffer.Buffer.Write(payload)
}
