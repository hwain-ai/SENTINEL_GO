package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/gotoolchain"
)

const runnerOutputLimit = 16 * 1024 * 1024
const privateEventPayloadLimit = 4000
const privateEventDrainLimit = time.Second

// Execute instruments only disposable test sources, runs fresh Go tests, and
// joins private typed events with official go test JSON events.
func Execute(parent context.Context, request Request) (Report, error) {
	validated, err := validateRunnerRequest(request)
	if err != nil {
		return Report{}, err
	}
	nonce, err := runnerNonce()
	if err != nil {
		return Report{}, err
	}
	plan, err := instrumentProject(validated.ProjectRoot, validated.CaptureReplay)
	if err != nil {
		return Report{}, err
	}
	restore, err := plan.apply()
	if err != nil {
		return Report{}, err
	}
	report, runErr := executeInstrumented(parent, validated, nonce, plan)
	// Check installed bytes before restoration can hide a test-side modification.
	verifyErr := plan.verifyInstalled()
	// Source verification/restoration errors take priority over cancellation so
	// an uncertain project state is never hidden behind a context error.
	if restoreErr := errors.Join(verifyErr, restore()); restoreErr != nil {
		return Report{}, fmt.Errorf("verify or restore test sources: %w", restoreErr)
	}
	if parentErr := parent.Err(); parentErr != nil {
		return Report{}, parentErr
	}
	return report, runErr
}

func validateRunnerRequest(request Request) (Request, error) {
	root, err := canonicalRunnerRoot(request.ProjectRoot)
	if err != nil {
		return Request{}, err
	}
	if request.Timeout <= 0 || request.Timeout > 24*time.Hour {
		return Request{}, fmt.Errorf("runner timeout is invalid")
	}
	goBinary, err := filepath.Abs(request.GoBinary)
	if err != nil {
		return Request{}, err
	}
	request.ProjectRoot = root
	request.GoBinary = filepath.Clean(goBinary)
	return request, nil
}

func canonicalRunnerRoot(projectRoot string) (string, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != filepath.Clean(root) {
		return "", fmt.Errorf("runner project root is not canonical")
	}
	return resolved, nil
}

func runnerNonce() (string, error) {
	payload := make([]byte, 16)
	if _, err := rand.Read(payload); err != nil {
		return "", fmt.Errorf("create runner nonce: %w", err)
	}
	return hex.EncodeToString(payload), nil
}

func executeInstrumented(parent context.Context, request Request, nonce string, plan *instrumentation) (Report, error) {
	ctx, cancel := context.WithTimeout(parent, request.Timeout)
	defer cancel()
	eventPath, collector, cleanup, err := prepareEventCollector()
	if err != nil {
		return Report{}, err
	}
	defer cleanup()
	defer collector.abort()
	environment, err := gotoolchain.OfflineEnvironment(request.GoBinary)
	if err != nil {
		return Report{}, err
	}
	environment = append(environment, "SENTINEL_GO_RUNNER_NONCE="+nonce, "SENTINEL_GO_RUNNER_EVENTS="+eventPath)
	stdout, runErr, timedOut, err := runGoTest(ctx, request, environment)
	if parentErr := parent.Err(); parentErr != nil {
		return Report{}, parentErr
	}
	if err != nil {
		return Report{}, err
	}
	// RISK(race): keep caller cancellation authoritative while allowing a
	// supervisor timeout a separate, bounded window to drain typed evidence.
	drainCtx, drainCancel := context.WithTimeout(parent, privateEventDrainLimit)
	private, err := finishPrivateEvents(drainCtx, collector)
	drainCancel()
	if parentErr := parent.Err(); parentErr != nil {
		return Report{}, parentErr
	}
	if err != nil {
		return Report{}, err
	}
	return assembleReport(nonce, plan, private, stdout, runErr, timedOut)
}

func assembleReport(nonce string, plan *instrumentation, private []PrivateEvent, stdout []byte, runErr error, timedOut bool) (Report, error) {
	inventory := plan.inventory
	assertions, terminal, err := separateAssertions(nonce, private, plan)
	if err != nil {
		return Report{}, err
	}
	official, cached, buildFailed, err := readOfficialEvents(stdout, inventory)
	if err != nil {
		return Report{}, err
	}
	status := classifiedRunStatus(nonce, inventory, terminal, official, runErr, timedOut, cached, buildFailed)
	report := Report{SchemaVersion: RunnerSchema, Nonce: nonce, Status: status, InventoryCount: len(inventory)}
	if plan.captureReplay {
		report.Replay = captureReplayIdentity(inventory, assertions, stdout, status)
		if report.Replay == nil {
			report.Status = StatusToolError
		}
	}
	return report, nil
}

func prepareEventCollector() (string, *eventCollector, func(), error) {
	eventPath, cleanup, err := createEventFile()
	if err != nil {
		return "", nil, nil, err
	}
	collector, err := startEventCollector(eventPath)
	if err != nil {
		cleanup()
		return "", nil, nil, err
	}
	return eventPath, collector, cleanup, nil
}

func finishPrivateEvents(ctx context.Context, collector *eventCollector) ([]PrivateEvent, error) {
	payload, err := collector.finish(ctx)
	if err != nil {
		return nil, err
	}
	return readPrivateEvents(payload)
}

func createEventFile() (string, func(), error) {
	directory, err := os.MkdirTemp("", "sentinel-go-runner-events-")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(directory, "events.pipe")
	cleanup := func() {
		_ = os.Remove(path)
		_ = os.Remove(directory)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

type eventReadResult struct {
	payload []byte
	err     error
}

type eventCollector struct {
	reader    *os.File
	keepalive *os.File
	done      chan eventReadResult
}

func startEventCollector(path string) (*eventCollector, error) {
	reader, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	keepalive, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		_ = reader.Close()
		return nil, err
	}
	// Keep the FIFO nonblocking so Go's poller can interrupt Read when Close runs.
	// Switching it to a blocking syscall would make cancellation itself hang.
	collector := &eventCollector{reader: reader, keepalive: keepalive, done: make(chan eventReadResult, 1)}
	go func() {
		payload, readErr := collectEventPayload(reader)
		collector.done <- eventReadResult{payload: payload, err: readErr}
	}()
	return collector, nil
}

func collectEventPayload(input io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(input, runnerOutputLimit+1))
	if len(payload) > runnerOutputLimit {
		return nil, fmt.Errorf("private event output exceeded limit")
	}
	return payload, err
}

func (collector *eventCollector) finish(ctx context.Context) ([]byte, error) {
	if collector.keepalive == nil {
		return nil, fmt.Errorf("private event collector already closed")
	}
	if err := collector.keepalive.Close(); err != nil {
		return nil, err
	}
	collector.keepalive = nil
	result := collector.await(ctx)
	if err := collector.reader.Close(); err != nil && result.err == nil {
		result.err = err
	}
	collector.reader = nil
	return result.payload, result.err
}

func (collector *eventCollector) await(ctx context.Context) eventReadResult {
	// RISK(security): a detached child may retain the FIFO after its parent exits.
	// Do not wait indefinitely for that writer to close the private evidence stream.
	select {
	case result := <-collector.done:
		return result
	case <-ctx.Done():
		return eventReadResult{err: ctx.Err()}
	}
}

func (collector *eventCollector) abort() {
	if collector.keepalive != nil {
		_ = collector.keepalive.Close()
		collector.keepalive = nil
	}
	if collector.reader != nil {
		_ = collector.reader.Close()
		collector.reader = nil
	}
}

func runGoTest(parent context.Context, request Request, environment []string) ([]byte, error, bool, error) {
	ctx, cancel := context.WithTimeout(parent, request.Timeout)
	defer cancel()
	arguments := []string{"test", "./...", "-count=1", "-json", "-timeout=0"}
	if request.CoverProfile != "" {
		arguments = append(arguments, "-coverprofile="+request.CoverProfile)
	}
	command := exec.CommandContext(ctx, request.GoBinary, arguments...)
	command.Dir = request.ProjectRoot
	command.Env = environment
	command.Stdin = nil
	var stdout boundedBuffer
	stdout.limit = runnerOutputLimit
	// Expose only Write so the embedded bytes.Buffer.ReadFrom cannot bypass
	// boundedBuffer.Write and its fail-closed output limit.
	output := struct{ io.Writer }{Writer: &stdout}
	command.Stdout = output
	command.Stderr = output
	runErr := runProcessGroup(command)
	if stdout.exceeded {
		return nil, nil, false, fmt.Errorf("go test output exceeded limit")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return stdout.Bytes(), runErr, true, nil
	}
	var exitError *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitError) {
		return nil, nil, false, fmt.Errorf("start go test: %w", runErr)
	}
	return stdout.Bytes(), runErr, false, nil
}

// RISK(side-effect): terminate only this command's new process group, including
// test descendants, before restoring or removing the disposable project.
func runProcessGroup(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = time.Second
	command.Cancel = func() error { return stopProcessGroup(command) }
	defer stopProcessGroup(command)
	return command.Run()
}

func stopProcessGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func readPrivateEvents(payload []byte) ([]PrivateEvent, error) {
	var events []PrivateEvent
	for len(payload) > 0 {
		if len(payload) < 4 {
			return nil, fmt.Errorf("private event prefix is truncated")
		}
		length := int(binary.BigEndian.Uint32(payload[:4]))
		payload = payload[4:]
		if length < 1 || length > privateEventPayloadLimit || length > len(payload) {
			return nil, fmt.Errorf("private event length is invalid")
		}
		event, err := decodePrivateEvent(payload[:length])
		if err != nil {
			return nil, err
		}
		events = append(events, event)
		payload = payload[length:]
	}
	return events, nil
}

func decodePrivateEvent(payload []byte) (PrivateEvent, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var event PrivateEvent
	if err := decoder.Decode(&event); err != nil {
		return PrivateEvent{}, fmt.Errorf("private event is invalid")
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return PrivateEvent{}, fmt.Errorf("private event has trailing data")
	}
	return event, nil
}

type goTestEvent struct {
	Action      string `json:"Action"`
	Package     string `json:"Package"`
	Test        string `json:"Test"`
	Cached      bool   `json:"Cached"`
	FailedBuild string `json:"FailedBuild"`
}

type officialEventAccumulator struct {
	known       map[string]struct{}
	events      []OfficialEvent
	cached      bool
	buildFailed bool
}

func readOfficialEvents(payload []byte, inventory []InventoryEntry) ([]OfficialEvent, bool, bool, error) {
	known := make(map[string]struct{}, len(inventory))
	for _, entry := range inventory {
		known[entry.ID] = struct{}{}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	accumulator := officialEventAccumulator{known: known}
	for {
		var raw goTestEvent
		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, false, fmt.Errorf("official go test event is invalid")
		}
		accumulator.add(raw)
	}
	return accumulator.events, accumulator.cached, accumulator.buildFailed, nil
}

func (accumulator *officialEventAccumulator) add(raw goTestEvent) {
	if raw.Cached {
		accumulator.cached = true
	}
	if raw.FailedBuild != "" {
		accumulator.buildFailed = true
	}
	id := raw.Package + "." + raw.Test
	if _, exists := accumulator.known[id]; !exists {
		return
	}
	if action, accepted := officialAction(raw.Action); accepted {
		accumulator.events = append(accumulator.events, OfficialEvent{TestID: id, Action: action})
	}
}

func officialAction(value string) (OfficialAction, bool) {
	switch value {
	case "run":
		return OfficialRun, true
	case "pass":
		return OfficialPass, true
	case "fail":
		return OfficialFail, true
	case "skip":
		return OfficialSkip, true
	default:
		return "", false
	}
}

func classifiedRunStatus(nonce string, inventory []InventoryEntry, private []PrivateEvent, official []OfficialEvent, runErr error, timedOut, cached, buildFailed bool) Status {
	if timedOut {
		return StatusTimedOut
	}
	if buildFailed {
		return StatusCompileError
	}
	if cached {
		return StatusToolError
	}
	if compileWithoutStructuredEvents(private, official, runErr) {
		return StatusCompileError
	}
	status := Classify(nonce, inventory, private, official)
	if (status == StatusPassed) != (runErr == nil) {
		return StatusToolError
	}
	return status
}

func compileWithoutStructuredEvents(private []PrivateEvent, official []OfficialEvent, runErr error) bool {
	return len(private) == 0 && len(official) == 0 && runErr != nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedBuffer) Write(payload []byte) (int, error) {
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
