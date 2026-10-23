package queryparams

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

func Decode(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var parameters map[string]any
	if err := decoder.Decode(&parameters); err != nil {
		return nil, fmt.Errorf("decoding query parameters: %w", err)
	}
	if parameters == nil {
		return nil, errors.New("query parameters must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("query parameters must contain one JSON object")
	}
	for key, value := range parameters {
		normalized, err := normalize(value)
		if err != nil {
			return nil, fmt.Errorf("query parameter %q: %w", key, err)
		}
		parameters[key] = normalized
	}
	return parameters, nil
}

func normalize(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		if !strings.ContainsAny(value.String(), ".eE") {
			number, err := value.Int64()
			if err != nil {
				return nil, fmt.Errorf("integer outside Neo4j's signed 64-bit range: %w", err)
			}
			return number, nil
		}
		number, err := value.Float64()
		if err != nil {
			return nil, fmt.Errorf("number outside Neo4j's floating-point range: %w", err)
		}
		return number, nil
	case []any:
		for i, item := range value {
			normalized, err := normalize(item)
			if err != nil {
				return nil, fmt.Errorf("list index %d: %w", i, err)
			}
			value[i] = normalized
		}
	case map[string]any:
		for key, item := range value {
			normalized, err := normalize(item)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", key, err)
			}
			value[key] = normalized
		}
	}
	return value, nil
}
