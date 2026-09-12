package orchestrator

import (
	"context"
	"slices"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/mutation"
)

type MutationRequest struct {
	ProjectRoot       string
	Sources           []string
	BackendExecutable string
	RunnerExecutable  string
	GoBinary          string
	Timeout           time.Duration
	MutantTimeout     time.Duration
}

type PublicMutant struct {
	CandidateID string          `json:"candidateId"`
	Status      mutation.Status `json:"status"`
}

type MutationReport struct {
	BackendCommit string              `json:"backendCommit"`
	Gate          mutation.GateResult `json:"gate"`
	Mutants       []PublicMutant      `json:"mutants"`
}

// RunMutation executes the vendored bridge through the snapshot adapter, then
// applies the local strict gate rather than trusting a backend score.
func RunMutation(ctx context.Context, request MutationRequest) (MutationReport, error) {
	sources, err := resolveMutationSources(request)
	if err != nil {
		return MutationReport{}, err
	}
	raw, err := runMutationBackend(ctx, request, sources)
	if err != nil {
		return MutationReport{}, err
	}
	return buildMutationReport(raw)
}

func resolveMutationSources(request MutationRequest) ([]string, error) {
	return ResolveMutationSourceInventory(request.ProjectRoot, request.Sources)
}

// ResolveMutationSourceInventory proves that an explicit mutation scope is the
// complete production inventory. Callers may omit the list to request safe
// discovery, but they may not narrow a strict project gate to easier files.
func ResolveMutationSourceInventory(projectRoot string, requested []string) ([]string, error) {
	discovered, err := productionGoSources(projectRoot)
	if err != nil {
		return nil, err
	}
	if len(requested) == 0 {
		return discovered, nil
	}
	explicit := append([]string(nil), requested...)
	slices.Sort(explicit)
	if !slices.Equal(explicit, discovered) {
		return nil, &mutation.AdapterError{
			Code:     "mutationSourceInventoryMismatch",
			ExitCode: 3,
		}
	}
	return explicit, nil
}

func runMutationBackend(ctx context.Context, request MutationRequest, sources []string) (mutation.BridgeReport, error) {
	return mutation.NewAdapter().Run(ctx, mutation.Request{
		ProjectRoot:       request.ProjectRoot,
		Sources:           sources,
		BackendExecutable: request.BackendExecutable,
		RunnerExecutable:  request.RunnerExecutable,
		GoBinary:          request.GoBinary,
		Timeout:           request.Timeout,
		MutantTimeout:     request.MutantTimeout,
	})
}

func buildMutationReport(raw mutation.BridgeReport) (MutationReport, error) {
	records, err := mutation.Normalize(raw)
	if err != nil {
		return MutationReport{}, err
	}
	gate, err := mutation.Evaluate(records, 0)
	if err != nil {
		return MutationReport{}, err
	}
	return MutationReport{
		BackendCommit: raw.BackendCommit,
		Gate:          gate,
		Mutants:       publicMutants(records),
	}, nil
}

func publicMutants(records []mutation.MutantRecord) []PublicMutant {
	result := make([]PublicMutant, 0, len(records))
	for _, record := range records {
		result = append(result, PublicMutant{CandidateID: record.CandidateID, Status: record.Status})
	}
	return result
}
