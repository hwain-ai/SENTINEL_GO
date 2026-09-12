package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/hwain-hwang/sentinel-go/internal/history"
	"github.com/hwain-hwang/sentinel-go/internal/orchestrator"
)

type runResult struct {
	RunID          string `json:"runId,omitempty"`
	Command        string `json:"command"`
	TerminalStatus string `json:"terminalStatus"`
}

type doctorResult struct {
	GoVersion     string `json:"goVersion"`
	Backend       string `json:"backend"`
	BackendCommit string `json:"backendCommit"`
	Pass          bool   `json:"pass"`
}

type historyResult struct {
	Runs            []history.RunRecord      `json:"runs,omitempty"`
	RepeatedDefects []history.RepeatedDefect `json:"repeatedDefects"`
}

type commandResult struct {
	SchemaVersion string                       `json:"schemaVersion"`
	Run           runResult                    `json:"run"`
	Crap          *orchestrator.CrapReport     `json:"crap,omitempty"`
	Mutation      *orchestrator.MutationReport `json:"mutation,omitempty"`
	Doctor        *doctorResult                `json:"doctor,omitempty"`
	History       *historyResult               `json:"history,omitempty"`
}

func writeCommandResult(output io.Writer, format string, result commandResult) error {
	if format == "json" {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	}
	_, err := fmt.Fprintln(output, renderText(result))
	return err
}

func renderText(result commandResult) string {
	if result.Doctor != nil {
		doctor := optionalValue(result.Doctor)
		return fmt.Sprintf("doctor: pass=%t go=%s backend=%s", doctor.Pass, doctor.GoVersion, doctor.Backend)
	}
	if result.History != nil {
		historyView := optionalValue(result.History)
		return fmt.Sprintf("history: runs=%d repeated-defects=%d", len(historyView.Runs), len(historyView.RepeatedDefects))
	}
	crapSummary := "not-run"
	if result.Crap != nil {
		crapReport := optionalValue(result.Crap)
		crapSummary = fmt.Sprintf("pass=%t callables=%d", crapReport.Pass, len(crapReport.Rows))
	}
	mutationSummary := "not-run"
	if result.Mutation != nil {
		mutationReport := optionalValue(result.Mutation)
		mutationSummary = fmt.Sprintf("pass=%t mutants=%d", mutationReport.Gate.Pass, mutationReport.Gate.InScope)
	}
	return fmt.Sprintf("%s: %s crap[%s] mutation[%s]", result.Run.Command, result.Run.TerminalStatus, crapSummary, mutationSummary)
}

func optionalValue[T any](value *T) T {
	switch value {
	case nil:
		var zero T
		return zero
	default:
		return *value
	}
}
