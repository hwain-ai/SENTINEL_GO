package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	ExitPassed           = 0
	ExitToolError        = 1
	ExitQualityFailed    = 2
	ExitUsageConfigError = 3
	ExitBaselineFailed   = 4
	ExitDependencyError  = 5
	ExitBackendError     = 6
	ExitEvidenceError    = 7
	ExitCancelled        = 8
)

const usage = `Usage: sentinel-go <command> [options]

Commands:
  crap       Run the native CRAP gate
  mutation   Run the mutate4go killed-only gate
  check      Run both CRAP and mutation gates
  all        Alias for check
  doctor     Verify the pinned runtime and mutation bridge
  history    Show project-local runs and repeated defects

Options:
  --project PATH       Go module root (default: current directory)
  --format text|json   Output format (default: text)
  --source PATH        Production Go file for mutation (repeatable)
  --backend PATH       Explicit mutate4go bridge executable
  --mutant-timeout-ms N Mutation/check/all: limit each candidate compile and replay
                        to 1..600000 ms (optional; overall backend limit: 10 minutes)
  --coverprofile PATH  Existing Go coverprofile for CRAP
  --repeated           History: show repeated defects only
  --help               Print this help without reading or writing project state
  --version            Print the installed artifact identity without project access
`

var toolVersion = "0.1.0"

const mutationBackendTimeout = 10 * time.Minute

type Options struct {
	Command       string
	Project       string
	Format        string
	Sources       []string
	Backend       string
	CoverProfile  string
	Repeated      bool
	Help          bool
	MutantTimeout time.Duration
}

// Parse accepts only the closed command and option vocabulary.
func Parse(arguments []string) (Options, error) {
	if hasArgument(arguments, "--help") || len(arguments) == 0 {
		return Options{Help: true}, nil
	}
	command, err := canonicalCommand(arguments[0])
	if err != nil {
		return Options{}, err
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return Options{}, fmt.Errorf("read current directory: %w", err)
	}
	options := Options{Command: command, Project: workingDirectory, Format: "text"}
	if err := consumeOptions(arguments[1:], &options); err != nil {
		return Options{}, err
	}
	return options, nil
}

func canonicalCommand(command string) (string, error) {
	switch command {
	case "crap", "mutation", "check", "doctor", "history":
		return command, nil
	case "all":
		return "check", nil
	default:
		return "", fmt.Errorf("unknown command %q", command)
	}
}

func consumeOptions(arguments []string, options *Options) error {
	for index := 0; index < len(arguments); index++ {
		name := arguments[index]
		switch name {
		case "--repeated":
			options.Repeated = true
		case "--project", "--format", "--source", "--backend", "--coverprofile", "--mutant-timeout-ms":
			if index+1 >= len(arguments) {
				return fmt.Errorf("missing value for %s", name)
			}
			index++
			if err := applyValueOption(options, name, arguments[index]); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown option %q", name)
		}
	}
	return validateOptions(*options)
}

func applyValueOption(options *Options, name, value string) error {
	if value == "" {
		return fmt.Errorf("empty value for %s", name)
	}
	switch name {
	case "--project":
		options.Project = filepath.Clean(value)
	case "--format":
		options.Format = value
	case "--source":
		options.Sources = append(options.Sources, filepath.ToSlash(value))
	case "--backend":
		options.Backend = filepath.Clean(value)
	case "--coverprofile":
		options.CoverProfile = filepath.Clean(value)
	case "--mutant-timeout-ms":
		return applyMutantTimeout(options, value)
	}
	return nil
}

func applyMutantTimeout(options *Options, value string) error {
	if options.MutantTimeout != 0 {
		return fmt.Errorf("duplicate --mutant-timeout-ms")
	}
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds < 1 || milliseconds > mutationBackendTimeout.Milliseconds() {
		return fmt.Errorf("--mutant-timeout-ms must be an integer in 1..600000")
	}
	options.MutantTimeout = time.Duration(milliseconds) * time.Millisecond
	return nil
}

func validateOptions(options Options) error {
	if options.Format != "text" && options.Format != "json" {
		return fmt.Errorf("format must be text or json")
	}
	checks := []struct {
		used    bool
		allowed bool
		message string
	}{
		{options.Repeated, options.Command == "history", "--repeated is only valid for history"},
		{len(options.Sources) > 0, commandIn(options.Command, "mutation", "check"), "--source is only valid for mutation or check"},
		{options.Backend != "", commandIn(options.Command, "mutation", "check", "doctor"), "--backend is not valid for this command"},
		{options.CoverProfile != "", commandIn(options.Command, "crap", "check"), "--coverprofile is only valid for crap or check"},
		{options.MutantTimeout != 0, commandIn(options.Command, "mutation", "check"), "--mutant-timeout-ms is only valid for mutation or check"},
	}
	for _, check := range checks {
		if check.used && !check.allowed {
			return fmt.Errorf("%s", check.message)
		}
	}
	return nil
}

func commandIn(command string, allowed ...string) bool {
	for _, candidate := range allowed {
		if command == candidate {
			return true
		}
	}
	return false
}

func hasArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
}

// Main is the testable CLI entry point.
func Main(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) == 1 && arguments[0] == "--version" {
		if _, err := fmt.Fprintf(stdout, "sentinel-go/%s\n", toolVersion); err != nil {
			fmt.Fprintln(stderr, "toolError")
			return ExitToolError
		}
		return ExitPassed
	}
	options, err := Parse(arguments)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitUsageConfigError
	}
	if options.Help {
		fmt.Fprint(stdout, usage)
		return ExitPassed
	}
	result, exitCode, err := execute(options)
	if err != nil {
		fmt.Fprintln(stderr, err)
	}
	if writeErr := writeCommandResult(stdout, options.Format, result); writeErr != nil {
		fmt.Fprintln(stderr, "toolError")
		return ExitToolError
	}
	return exitCode
}
