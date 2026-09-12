package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/runner"
)

const version = "sentinel-go-test-runner/1"

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
		fmt.Fprintln(stdout, version)
		return 0
	}
	request, err := parseRequest(arguments)
	if err != nil {
		fmt.Fprintln(stderr, "runnerUsageError")
		return 3
	}
	// RISK(process/cancellation): signal cancellation must reach the runner's
	// separately owned Go-test group and source restoration before CLI exit.
	report, err := runner.Execute(ctx, request)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		fmt.Fprintln(stderr, "runnerError")
		return 6
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(stderr, "runnerReportError")
		return 6
	}
	return 0
}

func parseRequest(arguments []string) (runner.Request, error) {
	set := flag.NewFlagSet("sentinel-go-test-runner", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	projectRoot := set.String("project-root", "", "project root")
	goBinary := set.String("go-binary", "", "Go executable")
	timeoutMilliseconds := set.Int64("timeout-ms", 0, "test timeout")
	coverProfile := set.String("coverprofile", "", "coverage output")
	if err := set.Parse(arguments); err != nil || len(set.Args()) != 0 || *projectRoot == "" || *goBinary == "" || *timeoutMilliseconds < 1 {
		return runner.Request{}, fmt.Errorf("invalid runner arguments")
	}
	return runner.Request{
		ProjectRoot:  *projectRoot,
		GoBinary:     *goBinary,
		Timeout:      time.Duration(*timeoutMilliseconds) * time.Millisecond,
		CoverProfile: *coverProfile,
	}, nil
}
