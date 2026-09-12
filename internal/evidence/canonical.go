package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func canonicalJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode canonical JSON: %w", err)
	}
	payload := output.Bytes()
	payload = bytes.ReplaceAll(payload, []byte(`\u2028`), []byte("\u2028"))
	payload = bytes.ReplaceAll(payload, []byte(`\u2029`), []byte("\u2029"))
	return append([]byte(nil), payload...), nil
}

func canonicalBody(value any) ([]byte, error) {
	payload, err := canonicalJSON(value)
	if err != nil {
		return nil, err
	}
	return payload[:len(payload)-1], nil
}

func decodeCanonical(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode canonical JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("canonical JSON has trailing data")
	}
	want, err := canonicalJSON(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, want) {
		return fmt.Errorf("JSON bytes are not canonical")
	}
	return nil
}
