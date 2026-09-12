package mutation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdapterRunsMachineBridgeOnlyInsideSnapshot(t *testing.T) {
	project := mutationTestProject(t)
	digest := mutationTestDigest(t, filepath.Join(project, "value.go"))
	backend := mutationTestBackend(t, fmt.Sprintf(`
/usr/bin/printf 'changed\n' > value.go
/usr/bin/printf 'package mutation\n\nfunc Positive(value int) bool { return value > 0 }\n' > value.go
/usr/bin/printf '%%s\n' '{"schemaVersion":"sentinel-mutate4go-report-v1","backendName":"mutate4go","backendCommit":"9016c7adafc1c7e282b5e27768e732e477713af8","sourceInventory":[{"path":"value.go","sha256":"%s","candidateCount":1}],"candidates":[{"id":"a","sourceFile":"value.go","line":3,"column":28,"operator":"> -> >="}],"outcomes":[{"candidateId":"a","status":"killed","durationNanos":1}]}'
`, digest))

	report, err := NewAdapter().Run(context.Background(), Request{
		ProjectRoot:       project,
		Sources:           []string{"value.go"},
		BackendExecutable: backend,
		RunnerExecutable:  backend,
		GoBinary:          filepath.Join(runtimeGOROOT(t), "bin", "go"),
		Timeout:           10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Candidates) != 1 || len(report.Outcomes) != 1 {
		t.Fatalf("report = %#v", report)
	}
	payload, err := os.ReadFile(filepath.Join(project, "value.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "func Positive") {
		t.Fatalf("original project was changed: %q", payload)
	}
}

func TestAdapterRejectsMalformedReportAndBackendFailure(t *testing.T) {
	project := mutationTestProject(t)
	request := Request{
		ProjectRoot:      project,
		Sources:          []string{"value.go"},
		GoBinary:         filepath.Join(runtimeGOROOT(t), "bin", "go"),
		RunnerExecutable: mutationTestBackend(t, "exit 0"),
		Timeout:          10 * time.Second,
	}

	request.BackendExecutable = mutationTestBackend(t, `/usr/bin/printf '{} trailing\n'`)
	if _, err := NewAdapter().Run(context.Background(), request); err == nil {
		t.Fatal("malformed report passed")
	}

	request.BackendExecutable = mutationTestBackend(t, `exit 6`)
	if _, err := NewAdapter().Run(context.Background(), request); err == nil {
		t.Fatal("failed backend passed")
	}
}

func TestAdapterRejectsDuplicateKeysBeforeNativeMapping(t *testing.T) {
	project := mutationTestProject(t)
	digest := mutationTestDigest(t, filepath.Join(project, "value.go"))
	body := fmt.Sprintf(`/usr/bin/printf '%%s\n' '{"schemaVersion":"sentinel-mutate4go-report-v1","backendName":"wrong","backendName":"mutate4go","backendCommit":"9016c7adafc1c7e282b5e27768e732e477713af8","sourceInventory":[{"path":"value.go","sha256":"%s","candidateCount":0}],"candidates":[],"outcomes":[]}'`, digest)
	_, err := NewAdapter().Run(context.Background(), Request{
		ProjectRoot:       project,
		Sources:           []string{"value.go"},
		BackendExecutable: mutationTestBackend(t, body),
		RunnerExecutable:  mutationTestBackend(t, "exit 0"),
		GoBinary:          filepath.Join(runtimeGOROOT(t), "bin", "go"),
		Timeout:           10 * time.Second,
	})
	if err == nil {
		t.Fatal("duplicate backendName was accepted")
	}
}

func TestValidateRequestRejectsMissingDuplicateAndInvalidInputs(t *testing.T) {
	backend := mutationTestBackend(t, "exit 0")
	goBinary := filepath.Join(runtimeGOROOT(t), "bin", "go")
	base := Request{BackendExecutable: backend, RunnerExecutable: backend, GoBinary: goBinary, Sources: []string{"value.go"}, Timeout: time.Second}
	cases := []Request{
		{BackendExecutable: backend, RunnerExecutable: backend, GoBinary: goBinary, Sources: []string{"value.go"}},
		{BackendExecutable: backend, RunnerExecutable: backend, GoBinary: goBinary, Timeout: time.Second},
		{BackendExecutable: backend, RunnerExecutable: backend, GoBinary: goBinary, Sources: []string{"value.go", "value.go"}, Timeout: time.Second},
		{BackendExecutable: backend, RunnerExecutable: backend, GoBinary: goBinary, Sources: []string{"../value.go"}, Timeout: time.Second},
	}
	for _, request := range cases {
		if _, err := validateRequest(request); err == nil {
			t.Fatalf("validateRequest(%+v) succeeded", request)
		}
	}
	if _, err := validateRequest(base); err != nil {
		t.Fatalf("valid request failed: %v", err)
	}
	if err := os.Chmod(backend, 0o722); err != nil {
		t.Fatal(err)
	}
	if _, err := trustedExecutable(backend); err == nil {
		t.Fatal("group-writable executable was accepted")
	}
}

func TestLimitedBufferBoundsOutputAndReportsOriginalWriteLength(t *testing.T) {
	buffer := &limitedBuffer{limit: 3}
	if count, err := buffer.Write([]byte("ab")); err != nil || count != 2 {
		t.Fatalf("first write = (%d, %v)", count, err)
	}
	if count, err := buffer.Write([]byte("cdef")); err != nil || count != 4 {
		t.Fatalf("bounded write = (%d, %v)", count, err)
	}
	if buffer.String() != "abc" || !buffer.exceeded {
		t.Fatalf("buffer = %q exceeded=%v", buffer.String(), buffer.exceeded)
	}
	if count, err := buffer.Write([]byte("z")); err != nil || count != 1 || buffer.String() != "abc" {
		t.Fatalf("full write = (%d, %v), buffer=%q", count, err, buffer.String())
	}
}

func mutationTestProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeMutationFile(t, filepath.Join(root, "go.mod"), "module example.test/mutation\n\ngo 1.27.0\n")
	writeMutationFile(t, filepath.Join(root, "value.go"), "package mutation\n\nfunc Positive(value int) bool { return value > 0 }\n")
	writeMutationFile(t, filepath.Join(root, "value_test.go"), "package mutation\n\nimport \"testing\"\nfunc TestPositive(t *testing.T) { if Positive(0) || !Positive(1) { t.Fatal(\"bad\") } }\n")
	return root
}

func mutationTestBackend(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridge")
	writeMutationFile(t, path, "#!/usr/bin/bash\nset -eu\n"+body+"\n")
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeMutationFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runtimeGOROOT(t *testing.T) string {
	t.Helper()
	root := os.Getenv("GOROOT")
	if root == "" {
		t.Fatal("GOROOT is empty")
	}
	return root
}

func mutationTestDigest(t *testing.T, path string) string {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
