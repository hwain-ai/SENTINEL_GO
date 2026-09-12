package evidence

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreCreatesAuthenticatedAppendOnlyBundle(t *testing.T) {
	project := t.TempDir()
	store := NewStore(project)
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	projectBytes, err := os.ReadFile(filepath.Join(store.Root(), "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	projectBytesAgain, err := os.ReadFile(filepath.Join(store.Root(), "project.json"))
	if err != nil || !bytes.Equal(projectBytes, projectBytesAgain) {
		t.Fatal("idempotent initialize changed the project root of trust")
	}
	var state projectState
	if err := decodeCanonical(projectBytes, &state); err != nil {
		t.Fatal(err)
	}

	fingerprint, err := store.Fingerprint("mutation", "survived", "private/module/value.go:Value")
	if err != nil {
		t.Fatal(err)
	}
	run := testRun(contractTestRunID, fingerprint)
	if err := store.Start(run.RunID, run.Command, run.OccurredAtUTC); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(run); err != nil {
		t.Fatal(err)
	}

	wantFiles := []string{
		"project.json",
		"commit.lock",
		"commit-sequence.json",
		filepath.Join("runs", run.RunID, "started.json"),
		filepath.Join("runs", run.RunID, "events", "00000000000000000000000000000001.json"),
		filepath.Join("runs", run.RunID, "evidence.json"),
	}
	for _, relative := range wantFiles {
		path := filepath.Join(store.Root(), relative)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("missing %s: %v", relative, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("unsafe mode for %s: %v", relative, info.Mode())
		}
	}
	if filepath.Base(store.Root()) != "state-v1" {
		t.Fatalf("state root = %q", store.Root())
	}

	runs, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].RunID != run.RunID || runs[0].CommitSequence != "1" {
		t.Fatalf("runs = %#v", runs)
	}

	privateCanary := "private/module/value.go:Value"
	for _, relative := range wantFiles[2:] {
		payload, err := os.ReadFile(filepath.Join(store.Root(), relative))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(payload, []byte(privateCanary)) {
			t.Fatalf("%s leaked semantic identity", relative)
		}
		for _, secret := range []string{state.ProjectIdentifier, state.FingerprintHMACKey, state.CleanupLeaseKey} {
			if bytes.Contains(payload, []byte(secret)) {
				t.Fatalf("%s leaked project secret", relative)
			}
		}
	}
}

func TestFingerprintIsStableOnlyInsideOneProject(t *testing.T) {
	first := NewStore(t.TempDir())
	second := NewStore(t.TempDir())
	for _, store := range []*Store{first, second} {
		if err := store.Initialize(); err != nil {
			t.Fatal(err)
		}
	}

	a, err := first.Fingerprint("crap", "aboveLimit", "callable")
	if err != nil {
		t.Fatal(err)
	}
	b, err := first.Fingerprint("crap", "aboveLimit", "callable")
	if err != nil {
		t.Fatal(err)
	}
	c, err := second.Fingerprint("crap", "aboveLimit", "callable")
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a == c {
		t.Fatalf("fingerprints first=%q repeated=%q second-project=%q", a, b, c)
	}
}

func TestLoadRejectsTamperWithoutWriting(t *testing.T) {
	tests := []struct {
		name     string
		relative string
		mutate   func([]byte) []byte
	}{
		{name: "project identifier", relative: "project.json", mutate: replaceProjectIdentifierCharacter},
		{name: "sequence", relative: "commit-sequence.json", mutate: replaceFirstOneWithZero},
		{name: "event", relative: filepath.Join("runs", contractTestRunID, "events", "00000000000000000000000000000001.json"), mutate: appendSpace},
		{name: "evidence", relative: filepath.Join("runs", contractTestRunID, "evidence.json"), mutate: appendSpace},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			store := populatedStore(t)
			path := filepath.Join(store.Root(), testCase.relative)
			payload, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, testCase.mutate(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			before := stateSnapshot(t, store.Root())
			if _, err := store.Load(); err == nil {
				t.Fatal("tampered state was accepted")
			}
			after := stateSnapshot(t, store.Root())
			if !equalSnapshot(before, after) {
				t.Fatal("read-only history changed tampered state")
			}
		})
	}
}

func TestHMACTamperFailsWithCanonicalJSON(t *testing.T) {
	t.Run("sequence", func(t *testing.T) {
		store := populatedStore(t)
		path := filepath.Join(store.Root(), "commit-sequence.json")
		var record sequenceRecord
		readCanonicalTestFile(t, path, &record)
		record.HMACSHA256 = flipHex(record.HMACSHA256)
		writeCanonicalTestFile(t, path, record)
		if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "HMAC") {
			t.Fatalf("sequence tamper error = %v", err)
		}
	})

	t.Run("evidence", func(t *testing.T) {
		store := populatedStore(t)
		path := filepath.Join(store.Root(), "runs", contractTestRunID, "evidence.json")
		var record evidenceRecord
		readCanonicalTestFile(t, path, &record)
		record.HMACSHA256 = flipHex(record.HMACSHA256)
		writeCanonicalTestFile(t, path, record)
		if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "HMAC") {
			t.Fatalf("evidence tamper error = %v", err)
		}
	})

	t.Run("event", func(t *testing.T) {
		store := populatedStore(t)
		runRoot := filepath.Join(store.Root(), "runs", contractTestRunID)
		eventPath := filepath.Join(runRoot, "events", "00000000000000000000000000000001.json")
		var event eventRecord
		readCanonicalTestFile(t, eventPath, &event)
		event.HMACSHA256 = flipHex(event.HMACSHA256)
		payload, err := canonicalJSON(event)
		if err != nil {
			t.Fatal(err)
		}
		var marker evidenceRecord
		readCanonicalTestFile(t, filepath.Join(runRoot, "evidence.json"), &marker)
		keys, err := readProjectKeys(filepath.Join(store.Root(), "project.json"))
		if err != nil {
			t.Fatal(err)
		}
		defer keys.clear()
		if _, err := decodeEvent(payload, event.EventID, marker, keys); err == nil || !strings.Contains(err.Error(), "HMAC") {
			t.Fatalf("event tamper error = %v", err)
		}
	})
}

func TestProjectStateBindingSurvivesRotationButNeverBypassesHMAC(t *testing.T) {
	before := projectKeys{
		projectIdentifier: bytes.Repeat([]byte{0x11}, 16),
		fingerprintKey:    bytes.Repeat([]byte{0x22}, 32),
		cleanupLeaseKey:   bytes.Repeat([]byte{0x33}, 32),
		keyEpoch:          1,
	}
	after := projectKeys{
		projectIdentifier: bytes.Clone(before.projectIdentifier),
		fingerprintKey:    bytes.Repeat([]byte{0x44}, 32),
		cleanupLeaseKey:   bytes.Clone(before.cleanupLeaseKey),
		keyEpoch:          2,
	}
	if projectStateHMAC(before) != projectStateHMAC(after) {
		t.Fatal("fingerprint key rotation changed the immutable project binding")
	}
	record := evidenceRecord{
		KeyEpoch:         1,
		ProjectStateHMAC: projectStateHMAC(before),
	}
	if !validStateBinding(record, after) {
		t.Fatal("authenticated old-epoch evidence was rejected after rotation")
	}
	record.ProjectStateHMAC = strings.Repeat("0", 64)
	if validStateBinding(record, after) {
		t.Fatal("old-epoch evidence bypassed project-state HMAC verification")
	}
}

func TestCanonicalDecoderRejectsDuplicateKeys(t *testing.T) {
	var value struct {
		Value int `json:"value"`
	}
	if err := decodeCanonical([]byte("{\"value\":1,\"value\":1}\n"), &value); err == nil {
		t.Fatal("duplicate JSON key was accepted")
	}
}

func TestSequenceDecimalParserUsesExactUint64Boundaries(t *testing.T) {
	valid := []string{"1", "9007199254740991", "9007199254740992", "18446744073709551615"}
	for _, value := range valid {
		if _, err := parseCanonicalUint64(value, false); err != nil {
			t.Fatalf("valid %q: %v", value, err)
		}
	}
	invalid := []string{"", "0", "00", "01", "+1", "1.0", "1e0", "1_0", "18446744073709551616"}
	for _, value := range invalid {
		if _, err := parseCanonicalUint64(value, false); err == nil {
			t.Fatalf("invalid %q was accepted", value)
		}
	}
}

func TestConcurrentAppendsAllocateUniqueMonotonicSequences(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := store.Fingerprint("mutation", "survived", "candidate")
	if err != nil {
		t.Fatal(err)
	}

	const count = 12
	start := make(chan struct{})
	errorsByRun := make(chan error, count)
	var group sync.WaitGroup
	for index := 0; index < count; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			run := testRun(fmt.Sprintf("00000000-0000-4000-8000-%012x", index+1), fingerprint)
			if err := store.Start(run.RunID, run.Command, run.OccurredAtUTC); err != nil {
				errorsByRun <- err
				return
			}
			errorsByRun <- store.Append(run)
		}()
	}
	close(start)
	group.Wait()
	close(errorsByRun)
	for err := range errorsByRun {
		if err != nil {
			t.Fatal(err)
		}
	}

	runs, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != count {
		t.Fatalf("run count = %d", len(runs))
	}
	for index, run := range runs {
		if run.CommitSequence != fmt.Sprint(index+1) {
			t.Fatalf("sequence[%d] = %q", index, run.CommitSequence)
		}
	}
}

func TestAllocatedCrashGapIsNeverReused(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	if err := store.withExistingState(lockExclusive, func(keys projectKeys) error {
		sequence, err := allocateSequence(store.Root(), keys.cleanupLeaseKey, 0)
		if err != nil || sequence != 1 {
			return fmt.Errorf("first allocation = %d, error = %v", sequence, err)
		}
		sequence, err = allocateSequence(store.Root(), keys.cleanupLeaseKey, 0)
		if err != nil || sequence != 2 {
			return fmt.Errorf("second allocation = %d, error = %v", sequence, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := store.Fingerprint("crap", "aboveLimit", "callable")
	if err != nil {
		t.Fatal(err)
	}
	run := testRun(contractTestRunID3, fingerprint)
	if err := store.Start(run.RunID, run.Command, run.OccurredAtUTC); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(run); err != nil {
		t.Fatal(err)
	}
	runs, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].CommitSequence != "3" {
		t.Fatalf("runs = %#v", runs)
	}
}

func TestPOSIXByteLockExcludesAnotherProcess(t *testing.T) {
	if helper := os.Getenv("SENTINEL_GO_LOCK_HELPER"); helper != "" {
		file, err := openLockFile(helper, false)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		err = tryExclusiveByteLock(file)
		if !errors.Is(err, errLockBusy) {
			t.Fatalf("child lock error = %v", err)
		}
		return
	}

	store := NewStore(t.TempDir())
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(store.Root(), "commit.lock")
	file, err := openLockFile(lockPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	unlock, err := lockByte(file, lockExclusive)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	command := exec.Command(os.Args[0], "-test.run=^TestPOSIXByteLockExcludesAnotherProcess$")
	command.Env = lockHelperEnvironment(lockPath)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("lock helper: %v\n%s", err, output)
	}
}

func TestLockHelperEnvironmentDoesNotInheritTypedRunnerChannel(t *testing.T) {
	t.Setenv("SENTINEL_GO_RUNNER_NONCE", "private-nonce")
	t.Setenv("SENTINEL_GO_RUNNER_EVENTS", "/private/events.pipe")
	environment := lockHelperEnvironment("/safe/commit.lock")
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "SENTINEL_GO_RUNNER_") {
		t.Fatalf("typed runner channel leaked to helper: %q", joined)
	}
	if !strings.Contains(joined, "SENTINEL_GO_LOCK_HELPER=/safe/commit.lock") {
		t.Fatalf("lock helper path missing: %q", joined)
	}
}

func lockHelperEnvironment(lockPath string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "SENTINEL_GO_RUNNER_NONCE=") || strings.HasPrefix(value, "SENTINEL_GO_RUNNER_EVENTS=") {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment, "SENTINEL_GO_LOCK_HELPER="+lockPath)
}

func TestTryExclusiveByteLockSucceedsWhenByteIsFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commit.lock")
	file, err := openLockFile(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := tryExclusiveByteLock(file); err != nil {
		t.Fatal(err)
	}
}

func TestUnsafeStatePathsFailClosed(t *testing.T) {
	t.Run("sentinel parent symlink", func(t *testing.T) {
		project := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(project, ".sentinel")); err != nil {
			t.Fatal(err)
		}
		if err := NewStore(project).Initialize(); err == nil {
			t.Fatal("symlinked state parent was accepted")
		}
	})

	t.Run("world-readable project state", func(t *testing.T) {
		store := populatedStore(t)
		path := filepath.Join(store.Root(), "project.json")
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(); err == nil {
			t.Fatal("unsafe project state permission was accepted")
		}
	})
}

func populatedStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(t.TempDir())
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := store.Fingerprint("mutation", "survived", "private-candidate")
	if err != nil {
		t.Fatal(err)
	}
	run := testRun(contractTestRunID, fingerprint)
	if err := store.Start(run.RunID, run.Command, run.OccurredAtUTC); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(run); err != nil {
		t.Fatal(err)
	}
	return store
}

func testRun(runID, fingerprint string) RunRecord {
	startedAt := time.Date(2026, 9, 3, 1, 2, 3, 456000000, time.UTC)
	return RunRecord{
		Certification:      false,
		Command:            "mutation",
		CommittedAtUTC:     startedAt.Add(2 * time.Second),
		CompletedAtUTC:     startedAt.Add(time.Second),
		Components:         Components{Mutation: &MutationComponent{InScope: 1, Survived: 1}},
		CorrelationID:      runID,
		DiagnosticCodes:    []string{"survivedMutant"},
		ExitCode:           2,
		FingerprintVersion: fingerprintVersion,
		Language:           "go",
		Mode:               "strict",
		ObservationSource:  "fresh",
		OccurredAtUTC:      startedAt,
		RunID:              runID,
		SchemaVersion:      RunSchemaVersion,
		SpecVersion:        specVersion,
		TerminalStatus:     "qualityFailed",
		Findings: []Finding{{
			Fingerprint: fingerprint,
			Component:   "mutation",
			Kind:        "survived",
		}},
	}
}

func appendSpace(payload []byte) []byte {
	return append(append([]byte(nil), payload...), ' ')
}

func replaceFirstOneWithZero(payload []byte) []byte {
	return bytes.Replace(payload, []byte(`"1"`), []byte(`"0"`), 1)
}

func replaceProjectIdentifierCharacter(payload []byte) []byte {
	marker := []byte(`"projectIdentifier":"`)
	start := bytes.Index(payload, marker)
	if start < 0 {
		return appendSpace(payload)
	}
	index := start + len(marker)
	result := append([]byte(nil), payload...)
	if result[index] == 'A' {
		result[index] = 'B'
	} else {
		result[index] = 'A'
	}
	return result
}

func stateSnapshot(t *testing.T, root string) map[string][]byte {
	t.Helper()
	snapshot := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot[relative] = payload
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func equalSnapshot(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	keys := make([]string, 0, len(left))
	for key := range left {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !bytes.Equal(left[key], right[key]) {
			return false
		}
	}
	return true
}

func readCanonicalTestFile(t *testing.T, path string, target any) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeCanonical(payload, target); err != nil {
		t.Fatal(err)
	}
}

func writeCanonicalTestFile(t *testing.T, path string, value any) {
	t.Helper()
	payload, err := canonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func flipHex(value string) string {
	replacement := byte('0')
	if value[len(value)-1] == replacement {
		replacement = '1'
	}
	return value[:len(value)-1] + string(replacement)
}

func TestAppendRejectsRawIdentityAndMissingStart(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	run := testRun(contractTestRunID, strings.Repeat("a", 64))
	run.Findings[0].RawIdentity = "private/path.go:Secret"
	if err := store.Append(run); err == nil {
		t.Fatal("raw identity was accepted")
	}
	run.Findings[0].RawIdentity = ""
	if err := store.Append(run); err == nil {
		t.Fatal("terminal commit without started marker was accepted")
	}
}
