package runner

type eventCounts struct {
	privateStart          int
	privateTerminal       EventKind
	terminalCount         int
	officialRun           int
	officialTerminal      OfficialAction
	officialTerminalCount int
}

// Classify joins the closed inventory with nonce-bound private events and
// official go test JSON events. Invalid structure fails closed.
func Classify(nonce string, inventory []InventoryEntry, private []PrivateEvent, official []OfficialEvent) Status {
	entries, counts, valid := initializeClassification(inventory)
	if !valid || !collectPrivateEvents(nonce, entries, counts, private) || !collectOfficialEvents(entries, counts, official) {
		return StatusToolError
	}
	mainComplete, mainRuntime, mainValid := classifyMainEvents(nonce, private, inventoryPackageCount(inventory))
	if !mainValid {
		return StatusToolError
	}
	status := StatusPassed
	for id, entry := range entries {
		candidate := classifyEntry(entry.Kind, counts[id], mainComplete)
		status = combineStatus(status, candidate)
	}
	if mainRuntime {
		return StatusRuntimeError
	}
	return status
}

func initializeClassification(inventory []InventoryEntry) (map[string]InventoryEntry, map[string]*eventCounts, bool) {
	entries := make(map[string]InventoryEntry, len(inventory))
	counts := make(map[string]*eventCounts, len(inventory))
	for _, entry := range inventory {
		if entry.ID == "" || !knownTestKind(entry.Kind) {
			return nil, nil, false
		}
		if _, duplicate := entries[entry.ID]; duplicate {
			return nil, nil, false
		}
		entries[entry.ID] = entry
		counts[entry.ID] = &eventCounts{}
	}
	return entries, counts, len(entries) > 0
}

func knownTestKind(kind TestKind) bool {
	return kind == KindTest || kind == KindFuzz || kind == KindExample
}

func collectPrivateEvents(nonce string, entries map[string]InventoryEntry, counts map[string]*eventCounts, events []PrivateEvent) bool {
	for _, event := range events {
		if event.Nonce != nonce {
			return false
		}
		if event.TestID == "" {
			continue
		}
		if _, exists := entries[event.TestID]; !exists || !addPrivateEvent(counts[event.TestID], event.Kind) {
			return false
		}
	}
	return true
}

func addPrivateEvent(counts *eventCounts, kind EventKind) bool {
	if kind == EventStart {
		if counts.terminalCount != 0 {
			return false
		}
		counts.privateStart++
		return counts.privateStart == 1
	}
	if !privateTerminal(kind) || counts.privateStart != 1 {
		return false
	}
	counts.terminalCount++
	counts.privateTerminal = kind
	return counts.terminalCount == 1
}

func privateTerminal(kind EventKind) bool {
	return kind == EventNormalReturn || kind == EventAssertionFailure || kind == EventPanic
}

func collectOfficialEvents(entries map[string]InventoryEntry, counts map[string]*eventCounts, events []OfficialEvent) bool {
	for _, event := range events {
		if _, exists := entries[event.TestID]; !exists || !addOfficialEvent(counts[event.TestID], event.Action) {
			return false
		}
	}
	return true
}

func addOfficialEvent(counts *eventCounts, action OfficialAction) bool {
	if action == OfficialRun {
		if counts.officialTerminalCount != 0 {
			return false
		}
		counts.officialRun++
		return counts.officialRun == 1
	}
	if !officialTerminal(action) || counts.officialRun != 1 {
		return false
	}
	counts.officialTerminalCount++
	counts.officialTerminal = action
	return counts.officialTerminalCount == 1
}

func officialTerminal(action OfficialAction) bool {
	return action == OfficialPass || action == OfficialFail || action == OfficialSkip
}

func classifyMainEvents(nonce string, events []PrivateEvent, packageCount int) (complete, runtimeFailure, valid bool) {
	counts, valid := collectMainEventCounts(nonce, events, packageCount)
	if !valid || contradictoryMainCounts(counts, packageCount) {
		return false, false, false
	}
	complete = completeMainCounts(counts, packageCount)
	runtimeFailure = counts[EventMainStart] > 0 && !complete
	return complete, runtimeFailure, true
}

func collectMainEventCounts(nonce string, events []PrivateEvent, packageCount int) (map[EventKind]int, bool) {
	counts := map[EventKind]int{}
	for _, event := range events {
		if event.TestID != "" {
			continue
		}
		if !addMainEventCount(nonce, event, packageCount, counts) {
			return nil, false
		}
	}
	return counts, true
}

func addMainEventCount(nonce string, event PrivateEvent, packageCount int, counts map[EventKind]int) bool {
	if event.Nonce != nonce || !mainEvent(event.Kind) {
		return false
	}
	counts[event.Kind]++
	if counts[event.Kind] > packageCount {
		return false
	}
	return orderedMainCounts(counts)
}

func orderedMainCounts(counts map[EventKind]int) bool {
	return counts[EventMainRunReturned] <= counts[EventMainStart] &&
		counts[EventMainNormalReturn] <= counts[EventMainRunReturned] &&
		counts[EventPanic] <= counts[EventMainStart]
}

func completeMainCounts(counts map[EventKind]int, packageCount int) bool {
	return counts[EventMainStart] == packageCount &&
		counts[EventMainRunReturned] == packageCount &&
		counts[EventMainNormalReturn] == packageCount &&
		counts[EventPanic] == 0
}

func contradictoryMainCounts(counts map[EventKind]int, packageCount int) bool {
	return counts[EventMainNormalReturn] == packageCount && counts[EventPanic] != 0
}

func inventoryPackageCount(inventory []InventoryEntry) int {
	packages := make(map[string]struct{})
	for _, entry := range inventory {
		separator := len(entry.ID) - len(lastIdentifier(entry.ID)) - 1
		if separator > 0 {
			packages[entry.ID[:separator]] = struct{}{}
		}
	}
	return len(packages)
}

func lastIdentifier(value string) string {
	for index := len(value) - 1; index >= 0; index-- {
		if value[index] == '.' {
			return value[index+1:]
		}
	}
	return value
}

func mainEvent(kind EventKind) bool {
	return kind == EventMainStart || kind == EventMainRunReturned || kind == EventMainNormalReturn || kind == EventPanic
}

func classifyEntry(kind TestKind, counts *eventCounts, mainComplete bool) Status {
	if counts.privateStart != 1 || counts.terminalCount != 1 || counts.officialRun != 1 || counts.officialTerminalCount != 1 {
		return StatusRuntimeError
	}
	if counts.privateTerminal == EventPanic || counts.officialTerminal == OfficialSkip {
		return StatusRuntimeError
	}
	if kind == KindExample {
		return classifyExample(counts, mainComplete)
	}
	return classifyTest(counts)
}

func classifyTest(counts *eventCounts) Status {
	if counts.privateTerminal == EventAssertionFailure && counts.officialTerminal == OfficialFail {
		return StatusAssertionFailure
	}
	if counts.privateTerminal == EventNormalReturn && counts.officialTerminal == OfficialPass {
		return StatusPassed
	}
	return StatusRuntimeError
}

func classifyExample(counts *eventCounts, mainComplete bool) Status {
	if exampleMismatch(counts, mainComplete) {
		return StatusAssertionFailure
	}
	if examplePassed(counts) {
		return StatusPassed
	}
	return StatusRuntimeError
}

func exampleMismatch(counts *eventCounts, mainComplete bool) bool {
	return counts.privateTerminal == EventNormalReturn && counts.officialTerminal == OfficialFail && mainComplete
}

func examplePassed(counts *eventCounts) bool {
	return counts.privateTerminal == EventNormalReturn && counts.officialTerminal == OfficialPass
}

func combineStatus(current, candidate Status) Status {
	if candidate == StatusRuntimeError || current == StatusRuntimeError {
		return StatusRuntimeError
	}
	if candidate == StatusAssertionFailure {
		return StatusAssertionFailure
	}
	return current
}
