package evidence

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

const (
	sequenceKeyNamespace = "SENTINEL\x00commit-sequence-key\x00v1\x00"
	sequenceMacNamespace = "SENTINEL\x00commit-sequence\x00v1\x00"
)

func readSequence(root string, cleanupKey []byte) (uint64, bool, error) {
	path := filepath.Join(root, "commit-sequence.json")
	payload, err := readOwnerFile(path)
	if errors.Is(err, os.ErrNotExist) || errors.Is(unwrapPathError(err), os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	record, value, err := decodeSequence(payload)
	if err != nil {
		return 0, false, err
	}
	if err := verifySequenceHMAC(record, cleanupKey); err != nil {
		return 0, false, err
	}
	return value, true, nil
}

func decodeSequence(payload []byte) (sequenceRecord, uint64, error) {
	var record sequenceRecord
	if err := decodeCanonical(payload, &record); err != nil {
		return sequenceRecord{}, 0, fmt.Errorf("invalid commit sequence: %w", err)
	}
	if record.Version != sequenceVersion || !hexDigest.MatchString(record.HMACSHA256) {
		return sequenceRecord{}, 0, fmt.Errorf("invalid commit sequence metadata")
	}
	value, err := parseCanonicalUint64(record.LastAllocated, false)
	if err != nil {
		return sequenceRecord{}, 0, fmt.Errorf("invalid commit sequence number: %w", err)
	}
	return record, value, nil
}

func verifySequenceHMAC(record sequenceRecord, cleanupKey []byte) error {
	body, err := canonicalBody(sequenceBody{LastAllocated: record.LastAllocated, Version: record.Version})
	if err != nil {
		return err
	}
	macKey := deriveHMACKey(cleanupKey, sequenceKeyNamespace)
	defer clear(macKey)
	want := namespacedHMAC(macKey, sequenceMacNamespace, body)
	if !secureEqualHex(record.HMACSHA256, want) {
		return fmt.Errorf("commit sequence HMAC mismatch")
	}
	return nil
}

func allocateSequence(root string, cleanupKey []byte, highWater uint64) (uint64, error) {
	current, exists, err := readSequence(root, cleanupKey)
	if err != nil {
		return 0, err
	}
	next, err := nextSequence(current, exists, highWater)
	if err != nil {
		return 0, err
	}
	payload, err := encodeSequence(next, cleanupKey)
	if err != nil {
		return 0, err
	}
	if err := writeSequence(root, payload, exists); err != nil {
		return 0, err
	}
	return next, nil
}

func nextSequence(current uint64, exists bool, highWater uint64) (uint64, error) {
	if !exists && highWater != 0 {
		return 0, fmt.Errorf("commit sequence is missing")
	}
	if current < highWater {
		return 0, fmt.Errorf("commit sequence rollback")
	}
	if current == math.MaxUint64 {
		return 0, fmt.Errorf("commit sequence exhausted")
	}
	return current + 1, nil
}

func encodeSequence(value uint64, cleanupKey []byte) ([]byte, error) {
	body := sequenceBody{LastAllocated: strconv.FormatUint(value, 10), Version: sequenceVersion}
	bodyPayload, err := canonicalBody(body)
	if err != nil {
		return nil, err
	}
	macKey := deriveHMACKey(cleanupKey, sequenceKeyNamespace)
	defer clear(macKey)
	record := sequenceRecord{
		HMACSHA256:    namespacedHMAC(macKey, sequenceMacNamespace, bodyPayload),
		LastAllocated: body.LastAllocated,
		Version:       body.Version,
	}
	payload, err := canonicalJSON(record)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func writeSequence(root string, payload []byte, exists bool) error {
	path := filepath.Join(root, "commit-sequence.json")
	var err error
	if exists {
		err = atomicReplace(path, payload)
	} else {
		err = publishNoReplace(path, payload)
	}
	return err
}

func parseCanonicalUint64(value string, allowZero bool) (uint64, error) {
	if !isCanonicalUnsignedDecimal(value) {
		return 0, fmt.Errorf("non-canonical unsigned integer")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid unsigned integer")
	}
	if !allowZero && parsed == 0 {
		return 0, fmt.Errorf("invalid unsigned integer")
	}
	return parsed, nil
}

func isCanonicalUnsignedDecimal(value string) bool {
	if invalidUnsignedPrefix(value) {
		return false
	}
	for _, character := range value {
		if !isDecimalDigit(character) {
			return false
		}
	}
	return true
}

func invalidUnsignedPrefix(value string) bool {
	return value == "" || (len(value) > 1 && value[0] == '0')
}

func isDecimalDigit(character rune) bool {
	return character >= '0' && character <= '9'
}

func unwrapPathError(err error) error {
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return pathError.Err
	}
	return err
}
