package evidence

import (
	"fmt"
	"math/big"
	"regexp"
	"sort"

	"github.com/hwain-hwang/sentinel-go/internal/runid"
)

var (
	canonicalNonnegativeDecimal = regexp.MustCompile(`^(?:0|[1-9][0-9]*)$`)
	canonicalPositiveDecimal    = regexp.MustCompile(`^[1-9][0-9]*$`)
	eventManifestFilename       = regexp.MustCompile(`^[0-9a-f]{32}\.json$`)
	safeDiagnosticCode          = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,63}$`)
	semanticVersion             = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
)

var terminalExitCodes = map[string]int{
	"passed":          0,
	"toolError":       1,
	"qualityFailed":   2,
	"baselineFailed":  4,
	"dependencyError": 5,
	"backendError":    6,
	"evidenceError":   7,
	"cancelled":       8,
}

func validateEvidenceBody(body evidenceBody, keys projectKeys, allowHistoricalEpoch bool) error {
	if err := validateEvidenceSemantics(body); err != nil {
		return err
	}
	return validateEvidenceProjectBinding(body, keys, allowHistoricalEpoch)
}

func validateEvidenceSemantics(body evidenceBody) error {
	if err := validateEvidenceIdentity(body); err != nil {
		return err
	}
	if err := validateEvidenceTimes(body); err != nil {
		return err
	}
	passes, err := validateComponents(body.Command, body.Components)
	if err != nil {
		return err
	}
	if err := validateTerminal(body, passes); err != nil {
		return err
	}
	if err := validateManifest(body); err != nil {
		return err
	}
	if err := validateDiagnostics(body.DiagnosticCodes); err != nil {
		return err
	}
	return nil
}

func validateEvidenceIdentity(body evidenceBody) error {
	checks := []func(evidenceBody) error{
		validateEvidenceVersions,
		validateEvidenceIDs,
		validateEvidenceEnums,
		validateEvidenceSequenceAndDigests,
		validateEvidenceSource,
	}
	for _, check := range checks {
		if err := check(body); err != nil {
			return err
		}
	}
	return nil
}

func validateEvidenceVersions(body evidenceBody) error {
	if body.SchemaVersion != evidenceSchemaVersion {
		return fmt.Errorf("evidenceSchemaVersionInvalid")
	}
	if !semanticVersion.MatchString(body.SpecVersion) {
		return fmt.Errorf("specVersionInvalid")
	}
	if body.FingerprintVersion != fingerprintVersion {
		return fmt.Errorf("fingerprintVersionInvalid")
	}
	return nil
}

func validateEvidenceIDs(body evidenceBody) error {
	if !runid.Valid(body.RunID) {
		return fmt.Errorf("runIdInvalid")
	}
	if !runid.Valid(body.CorrelationID) {
		return fmt.Errorf("correlationIdInvalid")
	}
	return nil
}

func validateEvidenceEnums(body evidenceBody) error {
	checks := []struct {
		value   string
		choices []string
		code    string
	}{
		{body.Command, []string{"crap", "mutation", "check"}, "commandInvalid"},
		{body.Language, []string{"python", "typescript", "go", "java", "clojure"}, "languageInvalid"},
		{body.Mode, []string{"strict", "local"}, "modeInvalid"},
		{body.ObservationSource, []string{"fresh", "cache"}, "observationSourceInvalid"},
	}
	for _, check := range checks {
		if !oneOf(check.value, check.choices...) {
			return fmt.Errorf("%s", check.code)
		}
	}
	return nil
}

func validateEvidenceSequenceAndDigests(body evidenceBody) error {
	if _, err := parseCanonicalUint64(body.CommitSequence, false); err != nil {
		return fmt.Errorf("commitSequenceInvalid")
	}
	if body.KeyEpoch == 0 {
		return fmt.Errorf("keyEpochInvalid")
	}
	if body.KeyEpoch > maxSafeInteger {
		return fmt.Errorf("keyEpochInvalid")
	}
	if !hexDigest.MatchString(body.ProjectStateHMAC) {
		return fmt.Errorf("projectStateHmacInvalid")
	}
	if !hexDigest.MatchString(body.StartedSHA256) {
		return fmt.Errorf("startedSha256Invalid")
	}
	return nil
}

func validateEvidenceSource(body evidenceBody) error {
	if err := validateSourceRun(body); err != nil {
		return err
	}
	if body.Mode == "strict" && body.ObservationSource == "cache" {
		return fmt.Errorf("strictCacheInvalid")
	}
	return nil
}

func validateSourceRun(body evidenceBody) error {
	if body.ObservationSource == "fresh" {
		if body.SourceRunID != nil {
			return fmt.Errorf("sourceRunIdInvalid")
		}
		return nil
	}
	if body.SourceRunID == nil || !runid.Valid(*body.SourceRunID) || *body.SourceRunID == body.RunID {
		return fmt.Errorf("sourceRunIdInvalid")
	}
	return nil
}

func validateEvidenceTimes(body evidenceBody) error {
	started, err := parseCanonicalUTC(body.StartedAtUTC)
	if err != nil {
		return fmt.Errorf("utcTimestampInvalid")
	}
	completed, err := parseCanonicalUTC(body.CompletedAtUTC)
	if err != nil {
		return fmt.Errorf("utcTimestampInvalid")
	}
	committed, err := parseCanonicalUTC(body.CommittedAtUTC)
	if err != nil {
		return fmt.Errorf("utcTimestampInvalid")
	}
	if completed.Before(started) || committed.Before(completed) {
		return fmt.Errorf("evidenceTimeOrderInvalid")
	}
	return nil
}

func validateComponents(command string, components Components) (bool, error) {
	if componentSelection(components) != command {
		return false, fmt.Errorf("evidenceComponentsInvalid")
	}
	if components.Crap != nil {
		if err := validateCrapComponent(*components.Crap); err != nil {
			return false, err
		}
	}
	if components.Mutation != nil {
		if err := validateMutationComponent(*components.Mutation); err != nil {
			return false, err
		}
	}
	return componentsPass(components), nil
}

func componentSelection(components Components) string {
	selection := ""
	if components.Crap != nil {
		selection = "crap"
	}
	if components.Mutation != nil {
		if selection != "" {
			return "check"
		}
		return "mutation"
	}
	return selection
}

func validateCrapComponent(component CrapComponent) error {
	numerator, denominator, err := validateCrapFraction(component)
	if err != nil {
		return err
	}
	if err := validateCrapInventory(component, numerator, denominator); err != nil {
		return err
	}
	return validateCrapPass(component, numerator, denominator)
}

func validateCrapFraction(component CrapComponent) (*big.Int, *big.Int, error) {
	numerator, err := parseDecimal(component.MaxNumerator, true, 96)
	if err != nil {
		return nil, nil, fmt.Errorf("crapComponentInvalid")
	}
	denominator, err := parseDecimal(component.MaxDenominator, false, 48)
	if err != nil {
		return nil, nil, fmt.Errorf("crapComponentInvalid")
	}
	if new(big.Int).GCD(nil, nil, numerator, denominator).Cmp(big.NewInt(1)) != 0 {
		return nil, nil, fmt.Errorf("crapComponentFractionInvalid")
	}
	return numerator, denominator, nil
}

func validateCrapInventory(component CrapComponent, numerator, denominator *big.Int) error {
	if component.CallableCount > maxSafeInteger {
		return fmt.Errorf("crapComponentInvalid")
	}
	if component.UnknownCount > component.CallableCount {
		return fmt.Errorf("crapComponentInvalid")
	}
	noKnownCallable := component.CallableCount == component.UnknownCount
	isZeroOverOne := numerator.Sign() == 0 && denominator.Cmp(big.NewInt(1)) == 0
	if noKnownCallable != isZeroOverOne {
		return fmt.Errorf("crapComponentInventoryInvalid")
	}
	return nil
}

func validateCrapPass(component CrapComponent, numerator, denominator *big.Int) error {
	limit := new(big.Int).Mul(big.NewInt(8), denominator)
	wantPass := component.CallableCount > 0 && component.UnknownCount == 0 && numerator.Cmp(limit) <= 0
	if component.Pass != wantPass {
		return fmt.Errorf("crapComponentSemanticsInvalid")
	}
	return nil
}

func parseDecimal(value string, allowZero bool, maximumLength int) (*big.Int, error) {
	pattern := canonicalPositiveDecimal
	if allowZero {
		pattern = canonicalNonnegativeDecimal
	}
	if len(value) > maximumLength || !pattern.MatchString(value) {
		return nil, fmt.Errorf("invalid decimal")
	}
	parsed, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return nil, fmt.Errorf("invalid decimal")
	}
	return parsed, nil
}

func validateMutationComponent(component MutationComponent) error {
	counts := []uint64{
		component.Killed,
		component.Survived,
		component.Uncovered,
		component.TimedOut,
		component.CompileError,
		component.RuntimeError,
		component.Pending,
		component.Ignored,
		component.ToolError,
	}
	if component.InScope > maxSafeInteger {
		return fmt.Errorf("mutationComponentInvalid")
	}
	if component.UnauthorizedExclusion > maxSafeInteger {
		return fmt.Errorf("mutationComponentInvalid")
	}
	if err := validateMutationCounts(counts, component.InScope); err != nil {
		return err
	}
	return validateMutationPass(component)
}

func validateMutationCounts(counts []uint64, inScope uint64) error {
	var total uint64
	for _, count := range counts {
		if count > maxSafeInteger {
			return fmt.Errorf("mutationComponentInvalid")
		}
		if total > maxSafeInteger-count {
			return fmt.Errorf("mutationComponentInvalid")
		}
		total += count
	}
	if total != inScope {
		return fmt.Errorf("mutationComponentInvalid")
	}
	return nil
}

func validateMutationPass(component MutationComponent) error {
	wantPass := component.InScope > 0 && component.Killed == component.InScope && component.UnauthorizedExclusion == 0
	if component.Pass != wantPass {
		return fmt.Errorf("mutationComponentSemanticsInvalid")
	}
	return nil
}

func validateTerminal(body evidenceBody, componentPass bool) error {
	checks := []func(evidenceBody, bool) error{
		validateTerminalExit,
		validateTerminalPrecedence,
		validateTerminalComponents,
		validateCertification,
	}
	for _, check := range checks {
		if err := check(body, componentPass); err != nil {
			return err
		}
	}
	return nil
}

func validateTerminalExit(body evidenceBody, _ bool) error {
	exitCode, exists := terminalExitCodes[body.TerminalStatus]
	if !exists || body.ExitCode != exitCode {
		return fmt.Errorf("terminalStatusExitCodeMismatch")
	}
	return nil
}

func validateTerminalPrecedence(body evidenceBody, _ bool) error {
	if body.Components.Mutation != nil && body.Components.Mutation.ToolError > 0 && body.TerminalStatus != "backendError" {
		return fmt.Errorf("terminalStatusPrecedenceInvalid")
	}
	return nil
}

func validateTerminalComponents(body evidenceBody, componentPass bool) error {
	if body.TerminalStatus == "passed" && !componentPass {
		return fmt.Errorf("terminalComponentMismatch")
	}
	if body.TerminalStatus == "qualityFailed" && componentPass {
		return fmt.Errorf("terminalComponentMismatch")
	}
	return nil
}

func validateCertification(body evidenceBody, componentPass bool) error {
	wantCertification := body.Mode == "strict" && body.ObservationSource == "fresh" && body.TerminalStatus == "passed" && componentPass
	if body.Certification != wantCertification {
		return fmt.Errorf("certificationInvalid")
	}
	return nil
}

func validateManifest(body evidenceBody) error {
	if body.EventCount > maxSafeInteger {
		return fmt.Errorf("eventManifestInvalid")
	}
	if body.Events == nil {
		return fmt.Errorf("eventManifestInvalid")
	}
	if body.EventCount != uint64(len(body.Events)) {
		return fmt.Errorf("eventManifestCountMismatch")
	}
	if err := validateManifestEntries(body.Events); err != nil {
		return err
	}
	if body.ObservationSource == "cache" && len(body.Events) != 0 {
		return fmt.Errorf("cacheObservationHasEvents")
	}
	return nil
}

func validateManifestEntries(events []eventManifestEntry) error {
	seen := make(map[string]struct{}, len(events))
	for index, entry := range events {
		if err := validateManifestEntry(entry); err != nil {
			return err
		}
		if _, duplicate := seen[entry.Filename]; duplicate {
			return fmt.Errorf("eventManifestDuplicate")
		}
		seen[entry.Filename] = struct{}{}
		want := fmt.Sprintf("%032x.json", index+1)
		if entry.Filename != want {
			return fmt.Errorf("eventManifestOrdinalInvalid")
		}
	}
	return nil
}

func validateManifestEntry(entry eventManifestEntry) error {
	if !eventManifestFilename.MatchString(entry.Filename) {
		return fmt.Errorf("eventManifestFilenameInvalid")
	}
	if !hexDigest.MatchString(entry.SHA256) {
		return fmt.Errorf("eventManifestDigestInvalid")
	}
	return nil
}

func validateDiagnostics(codes []string) error {
	if codes == nil || !sort.StringsAreSorted(codes) {
		return fmt.Errorf("diagnosticCodesInvalid")
	}
	for index, code := range codes {
		if !safeDiagnosticCode.MatchString(code) || (index > 0 && codes[index-1] == code) {
			return fmt.Errorf("diagnosticCodesInvalid")
		}
	}
	return nil
}

func validateEvidenceProjectBinding(body evidenceBody, keys projectKeys, allowHistoricalEpoch bool) error {
	if body.KeyEpoch > keys.keyEpoch || (!allowHistoricalEpoch && body.KeyEpoch != keys.keyEpoch) {
		return fmt.Errorf("keyEpochInvalid")
	}
	if !secureEqualHex(body.ProjectStateHMAC, projectStateHMAC(keys)) {
		return fmt.Errorf("projectStateBindingInvalid")
	}
	return nil
}

func componentsPass(components Components) bool {
	if components.Crap != nil && !components.Crap.Pass {
		return false
	}
	if components.Mutation != nil && !components.Mutation.Pass {
		return false
	}
	return components.Crap != nil || components.Mutation != nil
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func evidenceRecordFromBody(body evidenceBody, hmacSHA256 string) evidenceRecord {
	return evidenceRecord{
		Certification: body.Certification, Command: body.Command, CommitSequence: body.CommitSequence,
		CommittedAtUTC: body.CommittedAtUTC, CompletedAtUTC: body.CompletedAtUTC, Components: body.Components,
		CorrelationID: body.CorrelationID, DiagnosticCodes: body.DiagnosticCodes, EventCount: body.EventCount,
		Events: body.Events, ExitCode: body.ExitCode, FingerprintVersion: body.FingerprintVersion,
		HMACSHA256: hmacSHA256, KeyEpoch: body.KeyEpoch, Language: body.Language, Mode: body.Mode,
		ObservationSource: body.ObservationSource, ProjectStateHMAC: body.ProjectStateHMAC, RunID: body.RunID,
		SchemaVersion: body.SchemaVersion, SourceRunID: body.SourceRunID, SpecVersion: body.SpecVersion,
		StartedAtUTC: body.StartedAtUTC, StartedSHA256: body.StartedSHA256, TerminalStatus: body.TerminalStatus,
	}
}

func evidenceBodyFromRecord(record evidenceRecord) evidenceBody {
	return evidenceBody{
		Certification: record.Certification, Command: record.Command, CommitSequence: record.CommitSequence,
		CommittedAtUTC: record.CommittedAtUTC, CompletedAtUTC: record.CompletedAtUTC, Components: record.Components,
		CorrelationID: record.CorrelationID, DiagnosticCodes: record.DiagnosticCodes, EventCount: record.EventCount,
		Events: record.Events, ExitCode: record.ExitCode, FingerprintVersion: record.FingerprintVersion,
		KeyEpoch: record.KeyEpoch, Language: record.Language, Mode: record.Mode,
		ObservationSource: record.ObservationSource, ProjectStateHMAC: record.ProjectStateHMAC, RunID: record.RunID,
		SchemaVersion: record.SchemaVersion, SourceRunID: record.SourceRunID, SpecVersion: record.SpecVersion,
		StartedAtUTC: record.StartedAtUTC, StartedSHA256: record.StartedSHA256, TerminalStatus: record.TerminalStatus,
	}
}
