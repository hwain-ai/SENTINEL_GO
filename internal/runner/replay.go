package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

func hashReplayFields(fields []string) string {
	hash := sha256.New()
	for _, field := range fields {
		_ = binary.Write(hash, binary.BigEndian, uint64(len(field)))
		_, _ = io.WriteString(hash, field)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func separateAssertions(nonce string, events []PrivateEvent, plan *instrumentation) ([]PrivateEvent, []PrivateEvent, error) {
	var assertions, terminal []PrivateEvent
	for _, event := range events {
		if event.Kind != EventAssertionSite {
			if event.SiteID != "" {
				return nil, nil, fmt.Errorf("unexpected assertion identity")
			}
			terminal = append(terminal, event)
			continue
		}
		if !validAssertionEvent(nonce, event, plan) {
			return nil, nil, fmt.Errorf("assertion identity is invalid")
		}
		assertions = append(assertions, event)
	}
	return assertions, terminal, nil
}

func validAssertionEvent(nonce string, event PrivateEvent, plan *instrumentation) bool {
	if !plan.captureReplay || event.Nonce != nonce {
		return false
	}
	pkg, known := plan.sites[event.SiteID]
	return known && strings.HasPrefix(event.TestID, pkg+".") && testInventoryContains(plan.inventory, event.TestID)
}

func testInventoryContains(inventory []InventoryEntry, id string) bool {
	for _, entry := range inventory {
		if id == entry.ID || strings.HasPrefix(id, entry.ID+"/") {
			return true
		}
	}
	return false
}

func captureReplayIdentity(inventory []InventoryEntry, assertions []PrivateEvent, stdout []byte, status Status) *ReplayIdentity {
	results, failed, err := replayResults(stdout, inventory)
	if err != nil {
		return nil
	}
	if status == StatusPassed && (len(assertions) > 0 || len(failed) > 0) {
		return nil
	}
	inventoryFields := []string{"test-inventory-v1"}
	for _, entry := range inventory {
		inventoryFields = append(inventoryFields, hashReplayFields([]string{entry.ID, string(entry.Kind)}))
	}
	sort.Strings(inventoryFields)
	identity := &ReplayIdentity{InventorySHA256: hashReplayFields(inventoryFields), ResultsSHA256: hashReplayFields(results)}
	if status == StatusAssertionFailure {
		identity.FailureSHA256 = failureIdentity(inventory, assertions, failed)
		if identity.FailureSHA256 == "" {
			return nil
		}
	}
	return identity
}

func replayResults(stdout []byte, inventory []InventoryEntry) ([]string, []string, error) {
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	var results, failed []string
	for {
		var event goTestEvent
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			return nil, nil, err
		}
		results, failed = appendReplayResult(event, inventory, results, failed)
	}
	sort.Strings(results)
	sort.Strings(failed)
	return results, failed, nil
}

func appendReplayResult(event goTestEvent, inventory []InventoryEntry, results, failed []string) ([]string, []string) {
	id := event.Package + "." + event.Test
	if !testInventoryContains(inventory, id) {
		return results, failed
	}
	if action, known := officialAction(event.Action); known {
		results = append(results, hashReplayFields([]string{id, string(action)}))
	}
	if event.Action == string(OfficialFail) {
		failed = append(failed, id)
	}
	return results, failed
}

func failureIdentity(inventory []InventoryEntry, assertions []PrivateEvent, failed []string) string {
	if len(failed) == 0 {
		return ""
	}
	fields := []string{"assertion-failures-v1"}
	for _, id := range failed {
		if !failureHasSite(id, inventory, assertions) {
			return ""
		}
		fields = append(fields, hashReplayFields([]string{"failed", id}))
	}
	for _, assertion := range assertions {
		if !slices.Contains(failed, assertion.TestID) {
			return ""
		}
		fields = append(fields, hashReplayFields([]string{"site", assertion.TestID, assertion.SiteID}))
	}
	sort.Strings(fields)
	return hashReplayFields(fields)
}

func failureHasSite(id string, inventory []InventoryEntry, assertions []PrivateEvent) bool {
	for _, entry := range inventory {
		if entry.ID == id && entry.Kind == KindExample {
			return true
		}
	}
	for _, assertion := range assertions {
		if assertion.TestID == id || strings.HasPrefix(assertion.TestID, id+"/") {
			return true
		}
	}
	return false
}
