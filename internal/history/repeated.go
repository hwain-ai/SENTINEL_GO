package history

import "sort"

// RepeatedDefects folds observations without modifying historical run files.
func RepeatedDefects(runs []RunRecord) []RepeatedDefect {
	byFingerprint := make(map[string]RepeatedDefect)
	for _, run := range runs {
		for _, finding := range run.Findings {
			current := byFingerprint[finding.Fingerprint]
			current = addObservation(current, finding, run)
			byFingerprint[finding.Fingerprint] = current
		}
	}
	result := make([]RepeatedDefect, 0, len(byFingerprint))
	for _, defect := range byFingerprint {
		defect.Repeated = defect.ObservationCount > 1
		result = append(result, defect)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Fingerprint < result[right].Fingerprint })
	return result
}

func addObservation(current RepeatedDefect, finding Finding, run RunRecord) RepeatedDefect {
	if current.ObservationCount == 0 {
		current.Fingerprint = finding.Fingerprint
		current.Component = finding.Component
		current.Kind = finding.Kind
		current.FirstSeenUTC = run.OccurredAtUTC
		current.LastSeenUTC = run.OccurredAtUTC
	}
	// Store.Load orders records by authenticated commitSequence. Wall-clock UTC
	// is display data and may move backward, so it must not choose lifecycle order.
	current.LastSeenUTC = run.OccurredAtUTC
	current.ObservationCount++
	return current
}
