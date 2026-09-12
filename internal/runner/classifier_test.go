package runner

import "testing"

func TestClassifySeparatesAssertionPanicAndExampleMismatch(t *testing.T) {
	testInventory := []InventoryEntry{{ID: "example.test/p.TestFail", Kind: KindTest}}
	assertion := []PrivateEvent{
		{Nonce: "nonce", TestID: testInventory[0].ID, Kind: EventStart},
		{Nonce: "nonce", TestID: testInventory[0].ID, Kind: EventAssertionFailure},
		{Nonce: "nonce", Kind: EventMainStart},
		{Nonce: "nonce", Kind: EventMainRunReturned},
		{Nonce: "nonce", Kind: EventMainNormalReturn},
	}
	official := []OfficialEvent{{TestID: testInventory[0].ID, Action: OfficialRun}, {TestID: testInventory[0].ID, Action: OfficialFail}}
	if got := Classify("nonce", testInventory, assertion, official); got != StatusAssertionFailure {
		t.Fatalf("assertion status = %q", got)
	}

	panicEvents := append([]PrivateEvent(nil), assertion...)
	panicEvents[1].Kind = EventPanic
	if got := Classify("nonce", testInventory, panicEvents, official); got != StatusRuntimeError {
		t.Fatalf("panic status = %q", got)
	}

	exampleInventory := []InventoryEntry{{ID: "example.test/p.ExampleBad", Kind: KindExample}}
	exampleEvents := append([]PrivateEvent(nil), assertion...)
	exampleEvents[0].TestID = exampleInventory[0].ID
	exampleEvents[1].TestID = exampleInventory[0].ID
	exampleEvents[1].Kind = EventNormalReturn
	if got := Classify("nonce", exampleInventory, exampleEvents, []OfficialEvent{{TestID: exampleInventory[0].ID, Action: OfficialRun}, {TestID: exampleInventory[0].ID, Action: OfficialFail}}); got != StatusAssertionFailure {
		t.Fatalf("example mismatch status = %q", got)
	}
}

func TestClassifyFailsClosedOnMissingDuplicateOrWrongNonceEvents(t *testing.T) {
	inventory := []InventoryEntry{{ID: "example.test/p.TestOne", Kind: KindTest}}
	official := []OfficialEvent{{TestID: inventory[0].ID, Action: OfficialRun}, {TestID: inventory[0].ID, Action: OfficialPass}}
	complete := []PrivateEvent{
		{Nonce: "nonce", TestID: inventory[0].ID, Kind: EventStart},
		{Nonce: "nonce", TestID: inventory[0].ID, Kind: EventNormalReturn},
		{Nonce: "nonce", Kind: EventMainStart},
		{Nonce: "nonce", Kind: EventMainRunReturned},
		{Nonce: "nonce", Kind: EventMainNormalReturn},
	}
	if got := Classify("nonce", inventory, complete, official); got != StatusPassed {
		t.Fatalf("complete status = %q", got)
	}
	if got := Classify("nonce", inventory, complete[:1], official); got != StatusRuntimeError {
		t.Fatalf("missing terminal status = %q", got)
	}
	duplicate := append(append([]PrivateEvent(nil), complete...), complete[1])
	if got := Classify("nonce", inventory, duplicate, official); got != StatusToolError {
		t.Fatalf("duplicate status = %q", got)
	}
	wrongNonce := append([]PrivateEvent(nil), complete...)
	wrongNonce[0].Nonce = "stale"
	if got := Classify("nonce", inventory, wrongNonce, official); got != StatusToolError {
		t.Fatalf("wrong nonce status = %q", got)
	}
}

func TestClassifyRejectsOutOfOrderAndContradictoryEvents(t *testing.T) {
	inventory := []InventoryEntry{{ID: "example.test/p.TestOne", Kind: KindTest}}
	complete := []PrivateEvent{
		{Nonce: "nonce", TestID: inventory[0].ID, Kind: EventStart},
		{Nonce: "nonce", TestID: inventory[0].ID, Kind: EventNormalReturn},
		{Nonce: "nonce", Kind: EventMainStart},
		{Nonce: "nonce", Kind: EventMainRunReturned},
		{Nonce: "nonce", Kind: EventMainNormalReturn},
	}
	official := []OfficialEvent{{TestID: inventory[0].ID, Action: OfficialRun}, {TestID: inventory[0].ID, Action: OfficialPass}}

	privateOutOfOrder := append([]PrivateEvent(nil), complete...)
	privateOutOfOrder[0], privateOutOfOrder[1] = privateOutOfOrder[1], privateOutOfOrder[0]
	if got := Classify("nonce", inventory, privateOutOfOrder, official); got != StatusToolError {
		t.Fatalf("private terminal before start status = %q", got)
	}

	officialOutOfOrder := append([]OfficialEvent(nil), official...)
	officialOutOfOrder[0], officialOutOfOrder[1] = officialOutOfOrder[1], officialOutOfOrder[0]
	if got := Classify("nonce", inventory, complete, officialOutOfOrder); got != StatusToolError {
		t.Fatalf("official terminal before run status = %q", got)
	}

	mainPanicAfterComplete := append(append([]PrivateEvent(nil), complete...), PrivateEvent{Nonce: "nonce", Kind: EventPanic})
	if got := Classify("nonce", inventory, mainPanicAfterComplete, official); got != StatusToolError {
		t.Fatalf("complete main plus panic status = %q", got)
	}
}
