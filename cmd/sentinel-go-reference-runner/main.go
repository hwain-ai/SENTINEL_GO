package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/referenceexecution"
)

const version = "sentinel-go-reference-runner/1"

type executeFunc func(context.Context, referenceexecution.Request) (referenceexecution.Receipt, error)

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
	return runContextWithExecute(ctx, arguments, stdout, stderr, referenceexecution.Execute)
}

func runContextWithExecute(ctx context.Context, arguments []string, stdout, stderr io.Writer, execute executeFunc) int {
	if len(arguments) == 1 && arguments[0] == "--version" {
		payload := version + "\n"
		written, err := io.WriteString(stdout, payload)
		if err != nil || written != len(payload) {
			fmt.Fprintln(stderr, "referenceRunnerReportError")
			return 6
		}
		return 0
	}
	request, err := parseRequest(arguments)
	if err != nil {
		fmt.Fprintln(stderr, "referenceRunnerUsageError")
		return 3
	}
	receipt, err := execute(ctx, request)
	if err != nil {
		fmt.Fprintln(stderr, "referenceRunnerError")
		return 6
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		fmt.Fprintln(stderr, "referenceRunnerReportError")
		return 6
	}
	payload = append(payload, '\n')
	// Cancellation remains authoritative immediately before output.
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(stderr, "referenceRunnerError")
		return 6
	}
	written, err := stdout.Write(payload)
	if err != nil || written != len(payload) {
		fmt.Fprintln(stderr, "referenceRunnerReportError")
		return 6
	}
	return 0
}

func parseRequest(arguments []string) (referenceexecution.Request, error) {
	values := make(map[string]string, 4)
	for index := 0; index < len(arguments); index++ {
		name, value, hasValue := strings.Cut(arguments[index], "=")
		if !knownFlag(name) || values[name] != "" {
			return referenceexecution.Request{}, fmt.Errorf("invalid arguments")
		}
		if !hasValue {
			index++
			if index >= len(arguments) {
				return referenceexecution.Request{}, fmt.Errorf("invalid arguments")
			}
			value = arguments[index]
		}
		if value == "" {
			return referenceexecution.Request{}, fmt.Errorf("invalid arguments")
		}
		values[name] = value
	}
	if len(values) != 4 {
		return referenceexecution.Request{}, fmt.Errorf("invalid arguments")
	}
	timeout, err := parseMilliseconds(values["--timeout-ms"])
	if err != nil || !validRequestID(values["--request-id"]) {
		return referenceexecution.Request{}, fmt.Errorf("invalid arguments")
	}
	return referenceexecution.Request{
		ProjectRoot: values["--project-root"],
		GoBinary:    values["--go-binary"],
		RequestID:   values["--request-id"],
		Timeout:     time.Duration(timeout) * time.Millisecond,
	}, nil
}

func knownFlag(name string) bool {
	switch name {
	case "--project-root", "--go-binary", "--request-id", "--timeout-ms":
		return true
	default:
		return false
	}
}

func parseMilliseconds(value string) (uint64, error) {
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("invalid milliseconds")
		}
	}
	milliseconds, err := strconv.ParseUint(value, 10, 64)
	if err != nil || milliseconds < 1 || milliseconds > 86_400_000 {
		return 0, fmt.Errorf("invalid milliseconds")
	}
	return milliseconds, nil
}

func validRequestID(value string) bool {
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
