package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

const referenceRunnerOutputLimit = 16 * 1024

type referenceReplay struct {
	InventorySHA256 string `json:"inventorySha256"`
	ResultsSHA256   string `json:"resultsSha256"`
	FailureSHA256   string `json:"failureSha256"`
}

type referenceReceipt struct {
	SchemaVersion       string           `json:"schemaVersion"`
	Profile             string           `json:"profile"`
	RequestID           string           `json:"requestId"`
	TimeoutMilliseconds int64            `json:"timeoutMilliseconds"`
	RunnerSchema        string           `json:"runnerSchema"`
	Nonce               string           `json:"nonce"`
	Status              string           `json:"status"`
	InventoryCount      int              `json:"inventoryCount"`
	InputSHA256         string           `json:"inputSha256"`
	Replay              *referenceReplay `json:"replay"`
	ReferenceOnly       bool             `json:"referenceOnly"`
	Certified           bool             `json:"certified"`
}

func decodeReferenceReceipt(payload []byte) (referenceReceipt, error) {
	if len(payload) > referenceRunnerOutputLimit {
		return referenceReceipt{}, fmt.Errorf("reference receipt exceeds limit")
	}
	fields, err := decodeExactObject(payload, []string{
		"schemaVersion", "profile", "requestId", "timeoutMilliseconds", "runnerSchema", "nonce", "status",
		"inventoryCount", "inputSha256", "replay", "referenceOnly", "certified",
	})
	if err != nil {
		return referenceReceipt{}, err
	}
	var receipt referenceReceipt
	if receipt.SchemaVersion, err = decodeString(fields["schemaVersion"]); err != nil {
		return referenceReceipt{}, err
	}
	if receipt.Profile, err = decodeString(fields["profile"]); err != nil {
		return referenceReceipt{}, err
	}
	if receipt.RequestID, err = decodeString(fields["requestId"]); err != nil {
		return referenceReceipt{}, err
	}
	if receipt.TimeoutMilliseconds, err = decodePositiveInt64(fields["timeoutMilliseconds"], 86_400_000); err != nil {
		return referenceReceipt{}, err
	}
	if receipt.RunnerSchema, err = decodeString(fields["runnerSchema"]); err != nil {
		return referenceReceipt{}, err
	}
	if receipt.Nonce, err = decodeString(fields["nonce"]); err != nil {
		return referenceReceipt{}, err
	}
	if receipt.Status, err = decodeString(fields["status"]); err != nil {
		return referenceReceipt{}, err
	}
	inventory, err := decodePositiveInt64(fields["inventoryCount"], int64(maxInt()))
	if err != nil {
		return referenceReceipt{}, err
	}
	receipt.InventoryCount = int(inventory)
	if receipt.InputSHA256, err = decodeString(fields["inputSha256"]); err != nil {
		return referenceReceipt{}, err
	}
	if receipt.ReferenceOnly, err = decodeBool(fields["referenceOnly"]); err != nil {
		return referenceReceipt{}, err
	}
	if receipt.Certified, err = decodeBool(fields["certified"]); err != nil {
		return referenceReceipt{}, err
	}
	if !bytes.Equal(fields["replay"], []byte("null")) {
		replay, replayErr := decodeReferenceReplay(fields["replay"])
		if replayErr != nil {
			return referenceReceipt{}, replayErr
		}
		receipt.Replay = &replay
	}
	if err := validateReferenceReceipt(receipt); err != nil {
		return referenceReceipt{}, err
	}
	return receipt, nil
}

func validateReferenceReceipt(receipt referenceReceipt) error {
	if receipt.SchemaVersion != "sentinel-go-reference-execution-v1" || receipt.Profile != "replay-identity-v1" ||
		receipt.RunnerSchema != "sentinel-go-typed-runner-v1" || !receipt.ReferenceOnly || receipt.Certified ||
		receipt.InventoryCount < 1 || receipt.TimeoutMilliseconds < 1 || receipt.TimeoutMilliseconds > 86_400_000 ||
		!validLowerHex(receipt.RequestID, 32) || !validLowerHex(receipt.Nonce, 32) || !validLowerHex(receipt.InputSHA256, 64) ||
		!knownRunnerStatus(receipt.Status) || (receipt.Replay == nil && receipt.Status != "toolError") {
		return fmt.Errorf("reference receipt is invalid")
	}
	if receipt.Replay != nil {
		if !validLowerHex(receipt.Replay.InventorySHA256, 64) || !validLowerHex(receipt.Replay.ResultsSHA256, 64) {
			return fmt.Errorf("reference replay is invalid")
		}
		if receipt.Status == "assertionFailure" {
			if !validLowerHex(receipt.Replay.FailureSHA256, 64) {
				return fmt.Errorf("reference failure identity is invalid")
			}
		} else if receipt.Replay.FailureSHA256 != "" {
			return fmt.Errorf("reference failure identity is invalid")
		}
	}
	return nil
}

func decodeReferenceReplay(payload []byte) (referenceReplay, error) {
	fields, err := decodeExactObject(payload, []string{"inventorySha256", "resultsSha256", "failureSha256"})
	if err != nil {
		return referenceReplay{}, err
	}
	inventory, err := decodeString(fields["inventorySha256"])
	if err != nil {
		return referenceReplay{}, err
	}
	results, err := decodeString(fields["resultsSha256"])
	if err != nil {
		return referenceReplay{}, err
	}
	failure, err := decodeString(fields["failureSha256"])
	if err != nil {
		return referenceReplay{}, err
	}
	return referenceReplay{InventorySHA256: inventory, ResultsSHA256: results, FailureSHA256: failure}, nil
}

func decodeExactObject(payload []byte, names []string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("expected object")
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	fields := make(map[string]json.RawMessage, len(names))
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || !allowed[name] || fields[name] != nil {
			return nil, fmt.Errorf("invalid object key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid object value")
		}
		fields[name] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, fmt.Errorf("unterminated object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("object has trailing data")
	}
	if len(fields) != len(names) {
		return nil, fmt.Errorf("object field missing")
	}
	return fields, nil
}

func decodeString(payload []byte) (string, error) {
	if len(payload) < 2 || payload[0] != '"' {
		return "", fmt.Errorf("expected string")
	}
	var value string
	if err := json.Unmarshal(payload, &value); err != nil {
		return "", fmt.Errorf("expected string")
	}
	return value, nil
}

func decodeBool(payload []byte) (bool, error) {
	if bytes.Equal(payload, []byte("true")) {
		return true, nil
	}
	if bytes.Equal(payload, []byte("false")) {
		return false, nil
	}
	return false, fmt.Errorf("expected boolean")
}

func decodePositiveInt64(payload []byte, maximum int64) (int64, error) {
	if len(payload) == 0 {
		return 0, fmt.Errorf("expected integer")
	}
	for _, character := range payload {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("expected integer")
		}
	}
	value, err := strconv.ParseInt(string(payload), 10, 64)
	if err != nil || value < 1 || value > maximum {
		return 0, fmt.Errorf("integer is outside range")
	}
	return value, nil
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
