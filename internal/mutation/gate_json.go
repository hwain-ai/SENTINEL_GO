package mutation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var integerToken = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// DecodeAndEvaluate rejects duplicate keys and unsafe numeric tokens before
// converting the aggregate mutation gate input.
func DecodeAndEvaluate(payload []byte) (GateResult, error) {
	value, err := decodeStrictJSON(payload)
	if err != nil {
		return GateResult{}, err
	}
	root, counts, err := decodeGateInput(value)
	if err != nil {
		return GateResult{}, err
	}
	return evaluateDecodedGate(root, counts)
}

func decodeGateInput(value any) (map[string]any, map[string]int64, error) {
	root, ok := value.(map[string]any)
	if !ok || !exactObjectKeys(root, "counts", "inScope", "unauthorizedExclusion") {
		return nil, nil, fmt.Errorf("gateInputInvalid")
	}
	countsObject, ok := root["counts"].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("mutationCountNotInteger")
	}
	counts, err := decodeCounts(countsObject)
	if err != nil {
		return nil, nil, err
	}
	return root, counts, nil
}

func evaluateDecodedGate(root map[string]any, counts map[string]int64) (GateResult, error) {
	inScope, err := decodedInteger(root["inScope"], "inScopeNotInteger")
	if err != nil {
		return GateResult{}, err
	}
	exclusion, err := decodedInteger(root["unauthorizedExclusion"], "unauthorizedExclusionNotInteger")
	if err != nil {
		return GateResult{}, err
	}
	return EvaluateCounts(counts, inScope, exclusion)
}

func decodeCounts(object map[string]any) (map[string]int64, error) {
	counts := make(map[string]int64, len(object))
	for name, value := range object {
		count, err := decodedInteger(value, "mutationCountNotInteger")
		if err != nil {
			return nil, err
		}
		counts[name] = count
	}
	return counts, nil
}

func decodedInteger(value any, typeError string) (int64, error) {
	integer, ok := value.(int64)
	if !ok {
		return 0, fmt.Errorf("%s", typeError)
	}
	return integer, nil
}

func decodeStrictJSON(payload []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("jsonTrailingData")
	}
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("jsonInvalid")
	}
	switch typed := token.(type) {
	case json.Delim:
		return decodeDelimitedValue(decoder, typed)
	case json.Number:
		return decodeIntegerToken(string(typed))
	default:
		return typed, nil
	}
}

func decodeDelimitedValue(decoder *json.Decoder, delimiter json.Delim) (any, error) {
	switch delimiter {
	case '{':
		return decodeJSONObject(decoder)
	case '[':
		return decodeJSONArray(decoder)
	default:
		return nil, fmt.Errorf("jsonInvalid")
	}
}

func decodeJSONObject(decoder *json.Decoder) (map[string]any, error) {
	object := make(map[string]any)
	for decoder.More() {
		if err := decodeObjectEntry(decoder, object); err != nil {
			return nil, err
		}
	}
	if err := consumeDelimiter(decoder, '}'); err != nil {
		return nil, err
	}
	return object, nil
}

func decodeObjectEntry(decoder *json.Decoder, object map[string]any) error {
	keyToken, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("jsonInvalid")
	}
	key, ok := keyToken.(string)
	if !ok {
		return fmt.Errorf("jsonInvalid")
	}
	if _, duplicate := object[key]; duplicate {
		return fmt.Errorf("jsonDuplicateKey")
	}
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return err
	}
	object[key] = value
	return nil
}

func consumeDelimiter(decoder *json.Decoder, expected json.Delim) error {
	token, err := decoder.Token()
	if err != nil || token != expected {
		return fmt.Errorf("jsonInvalid")
	}
	return nil
}

func decodeJSONArray(decoder *json.Decoder) ([]any, error) {
	var values []any
	for decoder.More() {
		value, err := decodeJSONValue(decoder)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := consumeDelimiter(decoder, ']'); err != nil {
		return nil, err
	}
	return values, nil
}

func decodeIntegerToken(token string) (int64, error) {
	if !integerToken.MatchString(token) || token == "-0" {
		return 0, fmt.Errorf("jsonNumberNotInteger")
	}
	unsigned := strings.TrimPrefix(token, "-")
	maximum := strconv.FormatInt(maximumSafeCount, 10)
	if len(unsigned) > len(maximum) || (len(unsigned) == len(maximum) && unsigned > maximum) {
		return 0, fmt.Errorf("jsonIntegerOutOfRange")
	}
	value, err := strconv.ParseInt(token, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("jsonIntegerOutOfRange")
	}
	return value, nil
}

func exactObjectKeys(object map[string]any, expected ...string) bool {
	if len(object) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, exists := object[key]; !exists {
			return false
		}
	}
	return true
}
