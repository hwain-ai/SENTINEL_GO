package mutation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func withMutantTimeout(t *testing.T, request Request, timeout time.Duration) Request {
	t.Helper()
	request.MutantTimeout = timeout
	return request
}

func TestMutantTimeoutAdapterRejectsBeforeToolOrSnapshotAccess(t *testing.T) {
	for _, value := range []time.Duration{-time.Millisecond, time.Nanosecond, time.Millisecond / 2, 1500 * time.Microsecond, time.Second + time.Millisecond, 24*time.Hour + time.Millisecond} {
		t.Run(value.String(), func(t *testing.T) {
			request := withMutantTimeout(t, Request{Timeout: time.Second, ProjectRoot: "/missing-project", BackendExecutable: "/missing-tool"}, value)
			report, err := NewAdapter().Run(context.Background(), request)
			var classified *AdapterError
			if !errors.As(err, &classified) || classified.Code != "mutantTimeoutInvalid" || classified.ExitCode != 3 || report.SchemaVersion != "" {
				t.Fatalf("report=%+v error=%v classification=%+v", report, err, classified)
			}
		})
	}
}

func TestMutantTimeoutAdapterArgumentsAndValidBoundaries(t *testing.T) {
	backend := mutationTestBackend(t, "exit 0")
	base := Request{BackendExecutable: backend, RunnerExecutable: backend, GoBinary: filepath.Join(runtimeGOROOT(t), "bin", "go"), Sources: []string{"value.go"}, Timeout: 24 * time.Hour}
	legacy := []string{"--project-root", ".", "--go-binary", base.GoBinary, "--runner-binary", backend, "--timeout-ms", "86400000", "--source", "value.go"}
	for _, value := range []time.Duration{0, time.Millisecond, 1250 * time.Millisecond, 24 * time.Hour} {
		t.Run(value.String(), func(t *testing.T) {
			validated, err := validateRequest(withMutantTimeout(t, base, value))
			if err != nil {
				t.Fatal(err)
			}
			want := slices.Clone(legacy)
			if value > 0 {
				want = append(want, "--mutant-timeout-ms", fmt.Sprint(value.Milliseconds()))
			}
			if got := bridgeArguments(validated); !slices.Equal(got, want) {
				t.Fatalf("arguments=%q want=%q", got, want)
			}
		})
	}
}

func TestMutantTimeoutPreservesBackendTimeoutRange(t *testing.T) {
	for _, outer := range []time.Duration{0, -time.Millisecond, 24*time.Hour + time.Millisecond} {
		_, err := validateRequest(withMutantTimeout(t, Request{Timeout: outer}, time.Millisecond))
		if err == nil || err.Error() != "backendTimeoutInvalid" {
			t.Fatalf("outer=%s error=%v", outer, err)
		}
	}
}
