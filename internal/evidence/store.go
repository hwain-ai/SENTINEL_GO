package evidence

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hwain-hwang/sentinel-go/internal/runid"
)

const (
	eventKeyNamespace    = "SENTINEL\x00finding-event-key\x00v1\x00"
	eventMacNamespace    = "SENTINEL\x00finding-event\x00v1\x00"
	evidenceKeyNamespace = "SENTINEL\x00evidence-key\x00v1\x00"
	evidenceMacNamespace = "SENTINEL\x00evidence\x00v1\x00"
	stateBindNamespace   = "SENTINEL\x00project-state-binding\x00v1\x00"
)

var (
	hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
	safeLabel = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)
)

// Start durably publishes the run marker before a backend can execute.
func (store *Store) Start(runID, command string, startedAtUTC time.Time) error {
	if !runid.Valid(runID) || !safeLabel.MatchString(command) || !validUTC(startedAtUTC) {
		return fmt.Errorf("invalid started run")
	}
	return store.withExistingState(lockShared, func(projectKeys) error {
		return store.writeStarted(startedRecord{
			Command:       command,
			RunID:         runID,
			SchemaVersion: startedSchemaVersion,
			StartedAtUTC:  formatUTC(startedAtUTC),
		})
	})
}

func (store *Store) writeStarted(record startedRecord) error {
	runsRoot := filepath.Join(store.Root(), "runs")
	if err := validateOwnerDirectory(runsRoot); err != nil {
		return err
	}
	runRoot, err := createRunRoot(runsRoot, record.RunID)
	if err != nil {
		return err
	}
	payload, err := canonicalJSON(record)
	if err != nil {
		return err
	}
	if err := publishStarted(runRoot, payload); err != nil {
		return err
	}
	return createEventsRoot(runRoot)
}

func createRunRoot(runsRoot, runID string) (string, error) {
	runRoot := filepath.Join(runsRoot, runID)
	if err := os.Mkdir(runRoot, 0o700); err != nil {
		return "", fmt.Errorf("create run directory: %w", err)
	}
	if err := validateOwnerDirectory(runRoot); err != nil {
		return "", err
	}
	if err := syncDirectory(runsRoot); err != nil {
		return "", err
	}
	return runRoot, nil
}

func publishStarted(runRoot string, payload []byte) error {
	if err := createOwnerFile(filepath.Join(runRoot, "started.json"), payload); err != nil {
		return err
	}
	return syncDirectory(runRoot)
}

func createEventsRoot(runRoot string) error {
	eventsRoot := filepath.Join(runRoot, "events")
	if err := ensureOwnerDirectory(eventsRoot); err != nil {
		return err
	}
	return syncDirectory(runRoot)
}

// Append assigns a durable sequence, publishes immutable events, then evidence.
func (store *Store) Append(run RunRecord) error {
	if err := validateDraft(run); err != nil {
		return err
	}
	return store.withExistingState(lockExclusive, func(keys projectKeys) error {
		completed, highWater, err := store.readCompleted(keys)
		if err != nil {
			return err
		}
		if containsRun(completed, run.RunID) {
			return fmt.Errorf("evidence run already exists: %s", run.RunID)
		}
		startedPayload, err := store.validateStarted(run)
		if err != nil {
			return err
		}
		if err := store.requireEmptyTerminalArea(run.RunID); err != nil {
			return err
		}
		sequence, err := allocateSequence(store.Root(), keys.cleanupLeaseKey, highWater)
		if err != nil {
			return err
		}
		return store.publishTerminal(run, startedPayload, sequence, keys)
	})
}

// Load verifies every completed bundle under one shared POSIX byte lock.
func (store *Store) Load() ([]RunRecord, error) {
	rootInfo, err := os.Lstat(store.Root())
	if errors.Is(err, os.ErrNotExist) {
		return []RunRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect evidence root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("evidence root is unsafe")
	}
	var runs []RunRecord
	err = store.withExistingState(lockShared, func(keys projectKeys) error {
		loaded, highWater, err := store.readCompleted(keys)
		if err != nil {
			return err
		}
		allocated, exists, err := readSequence(store.Root(), keys.cleanupLeaseKey)
		if err != nil {
			return err
		}
		if (!exists && highWater != 0) || allocated < highWater {
			return fmt.Errorf("commit sequence rollback")
		}
		runs = loaded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return runs, nil
}

func (store *Store) validateStarted(run RunRecord) ([]byte, error) {
	runRoot := filepath.Join(store.Root(), "runs", run.RunID)
	if err := validateOwnerDirectory(runRoot); err != nil {
		return nil, fmt.Errorf("started run is missing or unsafe: %w", err)
	}
	payload, err := readOwnerFile(filepath.Join(runRoot, "started.json"))
	if err != nil {
		return nil, fmt.Errorf("read started run: %w", err)
	}
	var record startedRecord
	if err := decodeCanonical(payload, &record); err != nil {
		return nil, fmt.Errorf("invalid started run: %w", err)
	}
	want := startedRecord{Command: run.Command, RunID: run.RunID, SchemaVersion: startedSchemaVersion, StartedAtUTC: formatUTC(run.OccurredAtUTC)}
	if record != want {
		return nil, fmt.Errorf("started run does not match terminal run")
	}
	return payload, nil
}

func (store *Store) requireEmptyTerminalArea(runID string) error {
	runRoot := filepath.Join(store.Root(), "runs", runID)
	if _, err := os.Lstat(filepath.Join(runRoot, "evidence.json")); err == nil || !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("terminal evidence already exists or is unreadable")
	}
	eventsRoot := filepath.Join(runRoot, "events")
	if err := validateOwnerDirectory(eventsRoot); err != nil {
		return err
	}
	entries, err := os.ReadDir(eventsRoot)
	if err != nil {
		return fmt.Errorf("read event directory: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("terminal event area is not empty")
	}
	return nil
}

func (store *Store) publishTerminal(run RunRecord, startedPayload []byte, sequence uint64, keys projectKeys) error {
	sequenceText := strconv.FormatUint(sequence, 10)
	committedAt := run.CommittedAtUTC
	if committedAt.IsZero() {
		committedAt = run.CompletedAtUTC
	}
	manifest, err := store.publishEvents(run, sequenceText, committedAt, keys)
	if err != nil {
		return err
	}
	body := evidenceBody{
		Certification:      run.Certification,
		Command:            run.Command,
		CommitSequence:     sequenceText,
		CommittedAtUTC:     formatUTC(committedAt),
		CompletedAtUTC:     formatUTC(run.CompletedAtUTC),
		Components:         run.Components,
		CorrelationID:      run.CorrelationID,
		DiagnosticCodes:    run.DiagnosticCodes,
		EventCount:         uint64(len(manifest)),
		Events:             manifest,
		ExitCode:           run.ExitCode,
		FingerprintVersion: run.FingerprintVersion,
		KeyEpoch:           keys.keyEpoch,
		Language:           run.Language,
		Mode:               run.Mode,
		ObservationSource:  run.ObservationSource,
		ProjectStateHMAC:   projectStateHMAC(keys),
		RunID:              run.RunID,
		SchemaVersion:      evidenceSchemaVersion,
		SourceRunID:        run.SourceRunID,
		SpecVersion:        run.SpecVersion,
		StartedAtUTC:       formatUTC(run.OccurredAtUTC),
		StartedSHA256:      sha256Hex(startedPayload),
		TerminalStatus:     run.TerminalStatus,
	}
	if err := validateEvidenceBody(body, keys, false); err != nil {
		return fmt.Errorf("invalid terminal evidence: %w", err)
	}
	bodyPayload, err := canonicalBody(body)
	if err != nil {
		return err
	}
	macKey := deriveHMACKey(keys.cleanupLeaseKey, evidenceKeyNamespace)
	defer clear(macKey)
	record := evidenceRecordFromBody(body, namespacedHMAC(macKey, evidenceMacNamespace, bodyPayload))
	payload, err := canonicalJSON(record)
	if err != nil {
		return err
	}
	path := filepath.Join(store.Root(), "runs", run.RunID, "evidence.json")
	return publishNoReplace(path, payload)
}

func (store *Store) publishEvents(run RunRecord, sequence string, observedAt time.Time, keys projectKeys) ([]eventManifestEntry, error) {
	eventsRoot := filepath.Join(store.Root(), "runs", run.RunID, "events")
	findings := append([]Finding(nil), run.Findings...)
	sort.Slice(findings, func(left, right int) bool { return findings[left].Fingerprint < findings[right].Fingerprint })
	manifest := make([]eventManifestEntry, 0, len(findings))
	for index, finding := range findings {
		eventID := fmt.Sprintf("%032x", index+1)
		body := eventBody{
			CommitSequence:   sequence,
			Component:        finding.Component,
			EventID:          eventID,
			Fingerprint:      finding.Fingerprint,
			FingerprintEpoch: keys.keyEpoch,
			Kind:             finding.Kind,
			ObservedAtUTC:    formatUTC(observedAt),
			RunID:            run.RunID,
			SchemaVersion:    eventSchemaVersion,
		}
		payload, err := encodeEvent(body, keys.cleanupLeaseKey)
		if err != nil {
			return nil, err
		}
		filename := eventID + ".json"
		if err := createOwnerFile(filepath.Join(eventsRoot, filename), payload); err != nil {
			return nil, err
		}
		if err := syncDirectory(eventsRoot); err != nil {
			return nil, err
		}
		manifest = append(manifest, eventManifestEntry{Filename: filename, SHA256: sha256Hex(payload)})
	}
	return manifest, nil
}

func encodeEvent(body eventBody, cleanupKey []byte) ([]byte, error) {
	bodyPayload, err := canonicalBody(body)
	if err != nil {
		return nil, err
	}
	macKey := deriveHMACKey(cleanupKey, eventKeyNamespace)
	defer clear(macKey)
	record := eventRecord{
		CommitSequence:   body.CommitSequence,
		Component:        body.Component,
		EventID:          body.EventID,
		Fingerprint:      body.Fingerprint,
		FingerprintEpoch: body.FingerprintEpoch,
		HMACSHA256:       namespacedHMAC(macKey, eventMacNamespace, bodyPayload),
		Kind:             body.Kind,
		ObservedAtUTC:    body.ObservedAtUTC,
		RunID:            body.RunID,
		SchemaVersion:    body.SchemaVersion,
	}
	return canonicalJSON(record)
}

func (store *Store) readCompleted(keys projectKeys) ([]RunRecord, uint64, error) {
	runsRoot := filepath.Join(store.Root(), "runs")
	if err := validateOwnerDirectory(runsRoot); err != nil {
		return nil, 0, err
	}
	entries, err := os.ReadDir(runsRoot)
	if err != nil {
		return nil, 0, fmt.Errorf("read runs: %w", err)
	}
	runs := make([]RunRecord, 0, len(entries))
	tracker := sequenceTracker{runBySequence: make(map[uint64]string)}
	for _, entry := range entries {
		run, completed, err := store.readBundle(entry, keys)
		if err != nil {
			return nil, 0, err
		}
		if !completed {
			continue
		}
		if err := tracker.add(run); err != nil {
			return nil, 0, err
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(left, right int) bool { return runLess(runs[left], runs[right]) })
	return runs, tracker.highWater, nil
}

type sequenceTracker struct {
	runBySequence map[uint64]string
	highWater     uint64
}

func (tracker *sequenceTracker) add(run RunRecord) error {
	sequence, _ := parseCanonicalUint64(run.CommitSequence, false)
	if previous, duplicate := tracker.runBySequence[sequence]; duplicate {
		return fmt.Errorf("duplicate commit sequence %d in %s and %s", sequence, previous, run.RunID)
	}
	tracker.runBySequence[sequence] = run.RunID
	if sequence > tracker.highWater {
		tracker.highWater = sequence
	}
	return nil
}

func runLess(left, right RunRecord) bool {
	leftSequence, _ := strconv.ParseUint(left.CommitSequence, 10, 64)
	rightSequence, _ := strconv.ParseUint(right.CommitSequence, 10, 64)
	if leftSequence != rightSequence {
		return leftSequence < rightSequence
	}
	return left.RunID < right.RunID
}

func (store *Store) readBundle(entry fs.DirEntry, keys projectKeys) (RunRecord, bool, error) {
	if !validRunEntry(entry) {
		return RunRecord{}, false, fmt.Errorf("unexpected run entry: %s", entry.Name())
	}
	runRoot := filepath.Join(store.Root(), "runs", entry.Name())
	if err := validateOwnerDirectory(runRoot); err != nil {
		return RunRecord{}, false, err
	}
	startedPayload, started, err := readStarted(runRoot, entry.Name())
	if err != nil {
		return RunRecord{}, false, err
	}
	evidencePath := filepath.Join(runRoot, "evidence.json")
	exists, err := pathExists(evidencePath)
	if err != nil {
		return RunRecord{}, false, err
	}
	if !exists {
		return RunRecord{}, false, nil
	}
	record, err := store.readEvidence(evidencePath, startedPayload, started, keys)
	if err != nil {
		return RunRecord{}, false, err
	}
	return record, true, nil
}

func validRunEntry(entry fs.DirEntry) bool {
	return entry.Type()&os.ModeSymlink == 0 && entry.IsDir() && runid.Valid(entry.Name())
}

func readStarted(runRoot, runID string) ([]byte, startedRecord, error) {
	payload, err := readOwnerFile(filepath.Join(runRoot, "started.json"))
	if err != nil {
		return nil, startedRecord{}, fmt.Errorf("read started marker: %w", err)
	}
	var record startedRecord
	if err := decodeCanonical(payload, &record); err != nil {
		return nil, startedRecord{}, fmt.Errorf("invalid started marker: %w", err)
	}
	if !validStarted(record, runID) {
		return nil, startedRecord{}, fmt.Errorf("invalid started marker")
	}
	return payload, record, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect evidence marker: %w", err)
	}
	return true, nil
}

func (store *Store) readEvidence(path string, startedPayload []byte, started startedRecord, keys projectKeys) (RunRecord, error) {
	payload, err := readOwnerFile(path)
	if err != nil {
		return RunRecord{}, err
	}
	var record evidenceRecord
	if err := decodeCanonical(payload, &record); err != nil {
		return RunRecord{}, fmt.Errorf("invalid evidence marker: %w", err)
	}
	if err := validateEvidenceRecord(record, startedPayload, started, keys); err != nil {
		return RunRecord{}, err
	}
	findings, err := store.readManifest(filepath.Dir(path), record, keys)
	if err != nil {
		return RunRecord{}, err
	}
	startedAt, _ := time.Parse(time.RFC3339Nano, started.StartedAtUTC)
	completedAt, _ := time.Parse(time.RFC3339Nano, record.CompletedAtUTC)
	committedAt, _ := time.Parse(time.RFC3339Nano, record.CommittedAtUTC)
	return RunRecord{
		Certification:      record.Certification,
		Command:            record.Command,
		CommitSequence:     record.CommitSequence,
		CommittedAtUTC:     committedAt,
		CompletedAtUTC:     completedAt,
		Components:         record.Components,
		CorrelationID:      record.CorrelationID,
		DiagnosticCodes:    record.DiagnosticCodes,
		ExitCode:           record.ExitCode,
		FingerprintVersion: record.FingerprintVersion,
		KeyEpoch:           record.KeyEpoch,
		Language:           record.Language,
		Mode:               record.Mode,
		ObservationSource:  record.ObservationSource,
		OccurredAtUTC:      startedAt,
		RunID:              record.RunID,
		SchemaVersion:      RunSchemaVersion,
		SourceRunID:        record.SourceRunID,
		SpecVersion:        record.SpecVersion,
		TerminalStatus:     record.TerminalStatus,
		Findings:           findings,
	}, nil
}

func validateEvidenceRecord(record evidenceRecord, startedPayload []byte, started startedRecord, keys projectKeys) error {
	if !hexDigest.MatchString(record.HMACSHA256) {
		return fmt.Errorf("evidence metadata mismatch")
	}
	if err := verifyEvidenceHMAC(record, keys.cleanupLeaseKey); err != nil {
		return err
	}
	body := evidenceBodyFromRecord(record)
	if err := validateEvidenceBody(body, keys, true); err != nil {
		return fmt.Errorf("evidence metadata mismatch: %w", err)
	}
	if !evidenceMatchesStarted(record, startedPayload, started) {
		return fmt.Errorf("evidence metadata mismatch")
	}
	return nil
}

func evidenceMatchesStarted(record evidenceRecord, startedPayload []byte, started startedRecord) bool {
	return record.RunID == started.RunID && record.Command == started.Command &&
		record.StartedAtUTC == started.StartedAtUTC && record.StartedSHA256 == sha256Hex(startedPayload)
}

func validStateBinding(record evidenceRecord, keys projectKeys) bool {
	if !hexDigest.MatchString(record.ProjectStateHMAC) {
		return false
	}
	return secureEqualHex(record.ProjectStateHMAC, projectStateHMAC(keys))
}

func verifyEvidenceHMAC(record evidenceRecord, cleanupKey []byte) error {
	body := evidenceBodyFromRecord(record)
	bodyPayload, err := canonicalBody(body)
	if err != nil {
		return err
	}
	macKey := deriveHMACKey(cleanupKey, evidenceKeyNamespace)
	defer clear(macKey)
	if want := namespacedHMAC(macKey, evidenceMacNamespace, bodyPayload); !secureEqualHex(record.HMACSHA256, want) {
		return fmt.Errorf("evidence HMAC mismatch")
	}
	return nil
}

func projectStateHMAC(keys projectKeys) string {
	return namespacedHMAC(keys.cleanupLeaseKey, stateBindNamespace, keys.projectIdentifier)
}

func (store *Store) readManifest(runRoot string, evidence evidenceRecord, keys projectKeys) ([]Finding, error) {
	eventsRoot := filepath.Join(runRoot, "events")
	entries, err := validateManifestShape(eventsRoot, evidence)
	if err != nil {
		return nil, err
	}
	findings := make([]Finding, 0, len(entries))
	for index, manifest := range evidence.Events {
		finding, err := readManifestFinding(eventsRoot, entries[index], manifest, index, evidence, keys)
		if err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

func validateManifestShape(eventsRoot string, evidence evidenceRecord) ([]os.DirEntry, error) {
	if uint64(len(evidence.Events)) != evidence.EventCount {
		return nil, fmt.Errorf("evidence event count mismatch")
	}
	if err := validateOwnerDirectory(eventsRoot); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(eventsRoot)
	if err != nil {
		return nil, fmt.Errorf("read evidence events: %w", err)
	}
	if len(entries) != len(evidence.Events) {
		return nil, fmt.Errorf("evidence event manifest mismatch")
	}
	return entries, nil
}

func readManifestFinding(eventsRoot string, entry os.DirEntry, manifest eventManifestEntry, index int, evidence evidenceRecord, keys projectKeys) (Finding, error) {
	wantName := fmt.Sprintf("%032x.json", index+1)
	if manifest.Filename != wantName || entry.Name() != wantName || !hexDigest.MatchString(manifest.SHA256) {
		return Finding{}, fmt.Errorf("invalid evidence event manifest")
	}
	payload, err := readOwnerFile(filepath.Join(eventsRoot, wantName))
	if err != nil {
		return Finding{}, fmt.Errorf("read evidence event: %w", err)
	}
	if sha256Hex(payload) != manifest.SHA256 {
		return Finding{}, fmt.Errorf("evidence event digest mismatch")
	}
	return decodeEvent(payload, strings.TrimSuffix(wantName, ".json"), evidence, keys)
}

func decodeEvent(payload []byte, eventID string, evidence evidenceRecord, keys projectKeys) (Finding, error) {
	var record eventRecord
	if err := decodeCanonical(payload, &record); err != nil {
		return Finding{}, fmt.Errorf("invalid finding event: %w", err)
	}
	if err := validateEventRecord(record, eventID, evidence); err != nil {
		return Finding{}, err
	}
	if err := verifyEventHMAC(record, keys.cleanupLeaseKey); err != nil {
		return Finding{}, err
	}
	return Finding{Fingerprint: record.Fingerprint, Component: record.Component, Kind: record.Kind}, nil
}

func validateEventRecord(record eventRecord, eventID string, evidence evidenceRecord) error {
	if !eventIdentityMatches(record, eventID, evidence) || !validEventFields(record) {
		return fmt.Errorf("finding event metadata mismatch")
	}
	if !validEventTime(record.ObservedAtUTC, evidence.CommittedAtUTC) {
		return fmt.Errorf("invalid finding event time")
	}
	return nil
}

func validEventTime(observedAtUTC, committedAtUTC string) bool {
	_, err := parseCanonicalUTC(observedAtUTC)
	return err == nil && observedAtUTC == committedAtUTC
}

func eventIdentityMatches(record eventRecord, eventID string, evidence evidenceRecord) bool {
	return record.SchemaVersion == eventSchemaVersion && record.EventID == eventID && record.RunID == evidence.RunID &&
		record.CommitSequence == evidence.CommitSequence && record.FingerprintEpoch == evidence.KeyEpoch
}

func validEventFields(record eventRecord) bool {
	return hexDigest.MatchString(record.Fingerprint) && hexDigest.MatchString(record.HMACSHA256) &&
		safeLabel.MatchString(record.Component) && safeLabel.MatchString(record.Kind)
}

func verifyEventHMAC(record eventRecord, cleanupKey []byte) error {
	body := eventBody{
		CommitSequence: record.CommitSequence, Component: record.Component, EventID: record.EventID,
		Fingerprint: record.Fingerprint, FingerprintEpoch: record.FingerprintEpoch, Kind: record.Kind,
		ObservedAtUTC: record.ObservedAtUTC, RunID: record.RunID, SchemaVersion: record.SchemaVersion,
	}
	bodyPayload, err := canonicalBody(body)
	if err != nil {
		return err
	}
	macKey := deriveHMACKey(cleanupKey, eventKeyNamespace)
	defer clear(macKey)
	if want := namespacedHMAC(macKey, eventMacNamespace, bodyPayload); !secureEqualHex(record.HMACSHA256, want) {
		return fmt.Errorf("finding event HMAC mismatch")
	}
	return nil
}

func validateDraft(run RunRecord) error {
	if !validDraftIdentity(run) {
		return fmt.Errorf("invalid terminal run")
	}
	if !validDraftTimes(run) {
		return fmt.Errorf("invalid terminal run")
	}
	if err := validateEvidenceSemantics(draftEvidenceBody(run)); err != nil {
		return fmt.Errorf("invalid terminal run: %w", err)
	}
	return validateDraftFindings(run.Findings)
}

func validDraftIdentity(run RunRecord) bool {
	return validDraftMetadata(run) && run.CommitSequence == "" && run.KeyEpoch == 0
}

func validDraftMetadata(run RunRecord) bool {
	return run.SchemaVersion == RunSchemaVersion && runid.Valid(run.RunID)
}

func validDraftTimes(run RunRecord) bool {
	return validUTC(run.OccurredAtUTC) && validUTC(run.CompletedAtUTC) &&
		(run.CommittedAtUTC.IsZero() || validUTC(run.CommittedAtUTC))
}

func draftEvidenceBody(run RunRecord) evidenceBody {
	committedAt := run.CommittedAtUTC
	if committedAt.IsZero() {
		committedAt = run.CompletedAtUTC
	}
	events := make([]eventManifestEntry, len(run.Findings))
	for index := range events {
		events[index] = eventManifestEntry{Filename: fmt.Sprintf("%032x.json", index+1), SHA256: strings.Repeat("0", 64)}
	}
	return evidenceBody{
		Certification: run.Certification, Command: run.Command, CommitSequence: "1",
		CommittedAtUTC: formatUTC(committedAt), CompletedAtUTC: formatUTC(run.CompletedAtUTC), Components: run.Components,
		CorrelationID: run.CorrelationID, DiagnosticCodes: run.DiagnosticCodes, EventCount: uint64(len(events)), Events: events,
		ExitCode: run.ExitCode, FingerprintVersion: run.FingerprintVersion, KeyEpoch: 1, Language: run.Language,
		Mode: run.Mode, ObservationSource: run.ObservationSource, ProjectStateHMAC: strings.Repeat("0", 64), RunID: run.RunID,
		SchemaVersion: evidenceSchemaVersion, SourceRunID: run.SourceRunID, SpecVersion: run.SpecVersion,
		StartedAtUTC: formatUTC(run.OccurredAtUTC), StartedSHA256: strings.Repeat("0", 64), TerminalStatus: run.TerminalStatus,
	}
}

func validateDraftFindings(findings []Finding) error {
	seen := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		if !validFinding(finding) {
			return fmt.Errorf("invalid evidence finding")
		}
		if _, duplicate := seen[finding.Fingerprint]; duplicate {
			return fmt.Errorf("duplicate evidence finding")
		}
		seen[finding.Fingerprint] = struct{}{}
	}
	return nil
}

func validFinding(finding Finding) bool {
	return finding.RawIdentity == "" && hexDigest.MatchString(finding.Fingerprint) &&
		safeLabel.MatchString(finding.Component) && safeLabel.MatchString(finding.Kind)
}

func validStarted(record startedRecord, runID string) bool {
	if record.SchemaVersion != startedSchemaVersion || record.RunID != runID || !safeLabel.MatchString(record.Command) {
		return false
	}
	_, err := parseCanonicalUTC(record.StartedAtUTC)
	return err == nil
}

func parseCanonicalUTC(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Year() < 1 || parsed.Location() != time.UTC || formatUTC(parsed) != value {
		return time.Time{}, fmt.Errorf("non-canonical UTC timestamp")
	}
	return parsed, nil
}

func validUTC(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}

func formatUTC(value time.Time) string {
	return value.Format(time.RFC3339Nano)
}

func containsRun(runs []RunRecord, runID string) bool {
	for _, run := range runs {
		if run.RunID == runID {
			return true
		}
	}
	return false
}
