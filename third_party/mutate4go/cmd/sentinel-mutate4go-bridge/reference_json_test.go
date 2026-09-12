package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestReferenceReceiptScalarAndPresenceBoundaries(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(validReferenceReceipt), &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		t.Run("missing/"+key, func(t *testing.T) {
			copy := make(map[string]json.RawMessage)
			for k, v := range fields {
				if k != key {
					copy[k] = v
				}
			}
			payload, _ := json.Marshal(copy)
			if _, err := decodeReferenceReceipt(payload); err == nil {
				t.Fatal("missing field accepted")
			}
		})
		for _, value := range []string{"null", "[]", "{}"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				copy := make(map[string]json.RawMessage)
				for k, v := range fields {
					copy[k] = v
				}
				copy[key] = json.RawMessage(value)
				payload, _ := json.Marshal(copy)
				if _, err := decodeReferenceReceipt(payload); err == nil {
					t.Fatal("wrong scalar accepted")
				}
			})
		}
	}
	for _, value := range []string{"null", "[]", "{}"} {
		payload := strings.Replace(validReferenceReceipt, `"failureSha256":""`, `"failureSha256":`+value, 1)
		if _, err := decodeReferenceReceipt([]byte(payload)); err == nil {
			t.Errorf("failureSha256 %s accepted", value)
		}
	}
	for _, key := range []string{"timeoutMilliseconds", "inventoryCount"} {
		for _, value := range []string{"0", "-1", "+1", "01", "1.0", "1e0", `"1"`, "9223372036854775808"} {
			old := fmt.Sprintf(`"%s":%s`, key, fields[key])
			payload := strings.Replace(validReferenceReceipt, old, fmt.Sprintf(`"%s":%s`, key, value), 1)
			if _, err := decodeReferenceReceipt([]byte(payload)); err == nil {
				t.Errorf("%s %s accepted", key, value)
			}
		}
	}
	for n := 0; n < len(validReferenceReceipt); n++ {
		if _, err := decodeReferenceReceipt([]byte(validReferenceReceipt[:n])); err == nil {
			t.Fatalf("truncated receipt accepted at %d", n)
		}
	}
}

const validReferenceReceipt = `{"schemaVersion":"sentinel-go-reference-execution-v1","profile":"replay-identity-v1","requestId":"0123456789abcdef0123456789abcdef","timeoutMilliseconds":1000,"runnerSchema":"sentinel-go-typed-runner-v1","nonce":"fedcba9876543210fedcba9876543210","status":"passed","inventoryCount":1,"inputSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","replay":{"inventorySha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","resultsSha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","failureSha256":""},"referenceOnly":true,"certified":false}`

func TestReferenceReceiptDecoderAcceptsExactReceipt(t *testing.T) {
	receipt, err := decodeReferenceReceipt([]byte(validReferenceReceipt))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RequestID != "0123456789abcdef0123456789abcdef" || receipt.Replay == nil || receipt.Replay.ResultsSHA256 != strings.Repeat("c", 64) {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestReferenceReceiptDecoderRejectsContractViolations(t *testing.T) {
	cases := map[string]string{
		"unknown root key":          strings.Replace(validReferenceReceipt, `"certified":false`, `"certified":false,"extra":0`, 1),
		"duplicate root key":        strings.Replace(validReferenceReceipt, `"certified":false`, `"certified":false,"status":"passed"`, 1),
		"missing false":             strings.Replace(validReferenceReceipt, `,"certified":false`, "", 1),
		"missing replay":            strings.Replace(validReferenceReceipt, `,"replay":{"inventorySha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","resultsSha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","failureSha256":""}`, "", 1),
		"integer exponent":          strings.Replace(validReferenceReceipt, `"timeoutMilliseconds":1000`, `"timeoutMilliseconds":1e3`, 1),
		"wrong boolean type":        strings.Replace(validReferenceReceipt, `"referenceOnly":true`, `"referenceOnly":"true"`, 1),
		"uppercase id":              strings.Replace(validReferenceReceipt, "0123456789abcdef0123456789abcdef", "0123456789ABCDEF0123456789abcdef", 1),
		"unknown replay key":        strings.Replace(validReferenceReceipt, `"failureSha256":""`, `"failureSha256":"","extra":""`, 1),
		"duplicate replay key":      strings.Replace(validReferenceReceipt, `"failureSha256":""`, `"failureSha256":"","resultsSha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"`, 1),
		"nested root value":         strings.Replace(validReferenceReceipt, `"status":"passed"`, `"status":{"value":"passed"}`, 1),
		"array replay":              strings.Replace(validReferenceReceipt, `"replay":{"inventorySha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","resultsSha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","failureSha256":""}`, `"replay":[]`, 1),
		"trailing value":            validReferenceReceipt + `{}`,
		"assertion missing failure": strings.Replace(validReferenceReceipt, `"status":"passed"`, `"status":"assertionFailure"`, 1),
		"passed has failure":        strings.Replace(validReferenceReceipt, `"failureSha256":""`, `"failureSha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"`, 1),
		"null passed replay":        strings.Replace(validReferenceReceipt, `"replay":{"inventorySha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","resultsSha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","failureSha256":""}`, `"replay":null`, 1),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeReferenceReceipt([]byte(payload)); err == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
}

func TestReferenceReceiptDecoderAcceptsToolErrorWithNullReplay(t *testing.T) {
	payload := strings.Replace(validReferenceReceipt, `"status":"passed"`, `"status":"toolError"`, 1)
	payload = strings.Replace(payload, `"replay":{"inventorySha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","resultsSha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","failureSha256":""}`, `"replay":null`, 1)
	receipt, err := decodeReferenceReceipt([]byte(payload))
	if err != nil || receipt.Replay != nil {
		t.Fatalf("receipt=%+v error=%v", receipt, err)
	}
}

func TestReferenceReceiptDecoderRejectsOversize(t *testing.T) {
	payload := append([]byte(validReferenceReceipt), make([]byte, referenceRunnerOutputLimit)...)
	if _, err := decodeReferenceReceipt(payload); err == nil {
		t.Fatal("oversized receipt accepted")
	}
}

func TestReferenceReceiptNumericAndReplayFieldBoundaries(t *testing.T) {
	for _, value := range []string{"1", "86400000"} {
		payload := strings.Replace(validReferenceReceipt, `"timeoutMilliseconds":1000`, `"timeoutMilliseconds":`+value, 1)
		if _, err := decodeReferenceReceipt([]byte(payload)); err != nil {
			t.Errorf("timeout %s rejected: %v", value, err)
		}
	}
	payload := strings.Replace(validReferenceReceipt, `"timeoutMilliseconds":1000`, `"timeoutMilliseconds":86400001`, 1)
	if _, err := decodeReferenceReceipt([]byte(payload)); err == nil {
		t.Fatal("timeout overflow accepted")
	}
	payload = strings.Replace(validReferenceReceipt, `"inventoryCount":1`, fmt.Sprintf(`"inventoryCount":%d`, maxInt()), 1)
	if _, err := decodeReferenceReceipt([]byte(payload)); err != nil {
		t.Fatalf("maximum Go int rejected: %v", err)
	}
	for _, key := range []string{"inventorySha256", "resultsSha256", "failureSha256"} {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(validReferenceReceipt), &fields)
		var replay map[string]json.RawMessage
		_ = json.Unmarshal(fields["replay"], &replay)
		delete(replay, key)
		fields["replay"], _ = json.Marshal(replay)
		payload, _ := json.Marshal(fields)
		if _, err := decodeReferenceReceipt(payload); err == nil {
			t.Errorf("missing replay %s accepted", key)
		}
	}
	for _, status := range []string{"compileError", "runtimeError", "timedOut", "toolError"} {
		payload := strings.Replace(validReferenceReceipt, `"status":"passed"`, `"status":"`+status+`"`, 1)
		if _, err := decodeReferenceReceipt([]byte(payload)); err != nil {
			t.Errorf("%s rejected: %v", status, err)
		}
	}
	padding := strings.Repeat(" ", referenceRunnerOutputLimit-len(validReferenceReceipt))
	if _, err := decodeReferenceReceipt([]byte(validReferenceReceipt + padding)); err != nil {
		t.Fatal("exact receipt byte limit rejected")
	}
}
