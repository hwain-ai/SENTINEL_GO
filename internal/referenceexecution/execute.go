package referenceexecution

import (
	"context"
	"errors"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/runner"
	"github.com/hwain-hwang/sentinel-go/internal/workspace"
)

const (
	receiptSchema = "sentinel-go-reference-execution-v1"
	replayProfile = "replay-identity-v1"
)

var (
	errInvalidRequest = errors.New("reference execution request is invalid")
	errGuard          = errors.New("reference execution guard failed")
	errRunner         = errors.New("reference execution runner failed")
	errInputChanged   = errors.New("reference execution input changed")
	errCleanup        = errors.New("reference execution cleanup failed")
	errCancelled      = errors.New("reference execution cancelled")
)

type Request struct {
	ProjectRoot string
	GoBinary    string
	RequestID   string
	Timeout     time.Duration
}

type Replay struct {
	InventorySHA256 string `json:"inventorySha256"`
	ResultsSHA256   string `json:"resultsSha256"`
	FailureSHA256   string `json:"failureSha256"`
}

type Receipt struct {
	SchemaVersion       string        `json:"schemaVersion"`
	Profile             string        `json:"profile"`
	RequestID           string        `json:"requestId"`
	TimeoutMilliseconds int64         `json:"timeoutMilliseconds"`
	RunnerSchema        string        `json:"runnerSchema"`
	Nonce               string        `json:"nonce"`
	Status              runner.Status `json:"status"`
	InventoryCount      int           `json:"inventoryCount"`
	InputSHA256         string        `json:"inputSha256"`
	Replay              *Replay       `json:"replay"`
	ReferenceOnly       bool          `json:"referenceOnly"`
	Certified           bool          `json:"certified"`
}

// Execute captures a guarded input identity and invokes the existing typed
// runner with replay capture enabled.
func Execute(ctx context.Context, request Request) (Receipt, error) {
	return executeWith(ctx, request, dependencies{
		create: createProjectGuard,
		run:    runner.Execute,
	})
}

type projectGuard interface {
	ContentSHA256() string
	VerifyOriginal() error
	Remove() error
}

type dependencies struct {
	create func(string) (projectGuard, error)
	run    func(context.Context, runner.Request) (runner.Report, error)
}

func executeWith(ctx context.Context, request Request, dependencies dependencies) (Receipt, error) {
	if !validRequest(ctx, request) {
		return Receipt{}, errInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, cancellationError(err)
	}
	guard, err := dependencies.create(request.ProjectRoot)
	if err != nil {
		return Receipt{}, guardCreationError(ctx.Err())
	}
	if ctx.Err() != nil {
		verifyErr := guard.VerifyOriginal()
		removeErr := guard.Remove()
		return Receipt{}, lifecycleError(nil, verifyErr, removeErr, ctx.Err())
	}

	// RISK(authority): the snapshot is a guard and spare copy only. The caller's
	// supplied disposable tree remains the exact tree executed by the runner.
	inputSHA256 := guard.ContentSHA256()
	report, runErr := dependencies.run(ctx, runner.Request{
		ProjectRoot:   request.ProjectRoot,
		GoBinary:      request.GoBinary,
		Timeout:       request.Timeout,
		CaptureReplay: true,
	})
	verifyErr := guard.VerifyOriginal()
	removeErr := guard.Remove()
	cancelErr := ctx.Err()

	combined := lifecycleError(runErr, verifyErr, removeErr, cancelErr)
	if combined != nil {
		return Receipt{}, combined
	}

	// RISK(classification): CaptureReplay is opt-in and can preserve a toolError
	// with no replay identity. Do not retry without capture or reclassify it.
	receipt := Receipt{
		SchemaVersion:       receiptSchema,
		Profile:             replayProfile,
		RequestID:           request.RequestID,
		TimeoutMilliseconds: request.Timeout.Milliseconds(),
		RunnerSchema:        runner.RunnerSchema,
		Nonce:               report.Nonce,
		Status:              report.Status,
		InventoryCount:      report.InventoryCount,
		InputSHA256:         inputSHA256,
		ReferenceOnly:       true,
		Certified:           false,
	}
	if report.Replay != nil {
		receipt.Replay = &Replay{
			InventorySHA256: report.Replay.InventorySHA256,
			ResultsSHA256:   report.Replay.ResultsSHA256,
			FailureSHA256:   report.Replay.FailureSHA256,
		}
	}
	return receipt, nil
}

func createProjectGuard(root string) (projectGuard, error) {
	return workspace.Create(root)
}

func validRequest(ctx context.Context, request Request) bool {
	return ctx != nil && request.ProjectRoot != "" && request.GoBinary != "" && isValidRequestID(request.RequestID) &&
		request.Timeout >= time.Millisecond && request.Timeout <= 24*time.Hour && request.Timeout%time.Millisecond == 0
}

func isValidRequestID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func lifecycleError(runErr, verifyErr, removeErr, cancelErr error) error {
	var failures []error
	if runErr != nil {
		failures = append(failures, errRunner)
	}
	if verifyErr != nil {
		failures = append(failures, errInputChanged)
	}
	if removeErr != nil {
		failures = append(failures, errCleanup)
	}
	if cancelErr != nil {
		failures = append(failures, errCancelled, cancelErr)
	}
	return errors.Join(failures...)
}

func cancellationError(cause error) error {
	return errors.Join(errCancelled, cause)
}

func guardCreationError(cancelErr error) error {
	if cancelErr == nil {
		return errGuard
	}
	return errors.Join(errGuard, errCancelled, cancelErr)
}
