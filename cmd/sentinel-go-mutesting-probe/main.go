// sentinel-go-mutesting-probe is an opt-in, non-certifying external backend probe.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/gomutesting"
)

const usage = `Usage: sentinel-go-mutesting-probe --project PATH --source FILE --go-binary PATH [--timeout-ms N] [--mutant-timeout-ms N]
This experimental comparison-and-numbers profile never certifies a strict pass.
Repeat --source for explicit production files. Test runs share one overall deadline.
An explicit mutant timeout must be 1..3600000 ms, no greater than the overall
timeout, and limits each candidate replay, never the controls.
Snapshot copying, source discovery and cleanup are not immediately interruptible.
Reports are printed to stdout; no SENTINEL evidence or history is written.
Successful observations still exit 6 (backendNotAdmitted).
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, arguments []string, output, errors io.Writer) int {
	if len(arguments) == 1 && arguments[0] == "--help" {
		if _, err := io.WriteString(output, usage); err != nil {
			return 6
		}
		return 0
	}
	request, err := parse(arguments)
	if err != nil {
		fmt.Fprintln(errors, "goMutestingUsage")
		return 3
	}
	report, err := gomutesting.Run(ctx, request)
	if err != nil {
		fmt.Fprintln(errors, err.Error())
		return failureCode(err.Error())
	}
	if err := json.NewEncoder(output).Encode(report); err != nil {
		fmt.Fprintln(errors, "goMutestingOutputFailed")
		return 6
	}
	fmt.Fprintln(errors, "backendNotAdmitted")
	return 6
}

func parse(arguments []string) (gomutesting.Request, error) {
	request := gomutesting.Request{Timeout: time.Minute}
	if len(arguments) == 0 || len(arguments)%2 != 0 {
		return request, fmt.Errorf("usage")
	}
	seen := map[string]bool{}
	for index := 0; index < len(arguments); index += 2 {
		name, value := arguments[index], arguments[index+1]
		if err := parseOption(&request, seen, name, value); err != nil {
			return request, err
		}
	}
	if !completeRequest(request) {
		return request, fmt.Errorf("usage")
	}
	return request, nil
}

func completeRequest(request gomutesting.Request) bool {
	return request.ProjectRoot != "" && request.GoBinary != "" && len(request.Sources) > 0 &&
		request.MutantTimeout <= request.Timeout
}

func parseOption(request *gomutesting.Request, seen map[string]bool, name, value string) error {
	if value == "" || (seen[name] && name != "--source") {
		return fmt.Errorf("usage")
	}
	seen[name] = true
	return applyOption(request, name, value)
}

func applyOption(request *gomutesting.Request, name, value string) error {
	switch name {
	case "--project":
		request.ProjectRoot = value
	case "--source":
		request.Sources = append(request.Sources, value)
	case "--go-binary":
		request.GoBinary = value
	case "--timeout-ms":
		return applyTimeout(request, value)
	case "--mutant-timeout-ms":
		return applyMutantTimeout(request, value)
	default:
		return fmt.Errorf("usage")
	}
	return nil
}

func applyTimeout(request *gomutesting.Request, value string) error {
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds < 1 || milliseconds > 3_600_000 {
		return fmt.Errorf("usage")
	}
	request.Timeout = time.Duration(milliseconds) * time.Millisecond
	return nil
}

func applyMutantTimeout(request *gomutesting.Request, value string) error {
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds < 1 || milliseconds > 3_600_000 {
		return fmt.Errorf("usage")
	}
	request.MutantTimeout = time.Duration(milliseconds) * time.Millisecond
	return nil
}

func failureCode(code string) int {
	switch code {
	case "goMutestingBaselineFailed":
		return 4
	case "goMutestingDependencyInvalid":
		return 5
	case "goMutestingCancelled":
		return 8
	case "goMutestingRequestInvalid", "goMutestingSourceInvalid", "goMutestingSourceMissing":
		return 3
	default:
		return 6
	}
}
