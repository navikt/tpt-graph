package workspace

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"tpt-graph/query-workspace/internal/queryparams"
)

type saveView struct {
	Status          string
	Name, Category  string
	Error           string
	NameInvalid     bool
	CategoryInvalid bool
}

type saveContext struct {
	Investigation Investigation
	Parameters    json.RawMessage
	Diagnostics   *GenerationDiagnostics
	Error         string
}

// Keep the previous run through a plain form POST without storing results in the catalogue.
// Authentication prevents posted rows or phase metadata from being presented as a forged run.
func (h *HTTPHandler) encodeContext(data pageData) (string, error) {
	state := saveContext{
		Investigation: *data.Investigation,
		Parameters:    json.RawMessage(data.GeneratedJSON),
		Diagnostics:   data.Investigation.Query.Diagnostics,
		Error:         data.Error,
	}
	state.Investigation.Query.Parameters = nil
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, h.contextKey)
	_, _ = mac.Write(encoded)
	return base64.RawURLEncoding.EncodeToString(encoded) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (h *HTTPHandler) decodeContext(token string) (saveContext, error) {
	payload, signature, ok := strings.Cut(token, ".")
	encoded, err := base64.RawURLEncoding.DecodeString(payload)
	if !ok || err != nil {
		return saveContext{}, errors.New("invalid investigation context")
	}
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return saveContext{}, errors.New("invalid investigation signature")
	}
	mac := hmac.New(sha256.New, h.contextKey)
	_, _ = mac.Write(encoded)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return saveContext{}, errors.New("investigation context signature mismatch")
	}
	var state saveContext
	if err := json.Unmarshal(encoded, &state); err != nil {
		return saveContext{}, err
	}
	parameters, err := queryparams.Decode(state.Parameters)
	if err != nil {
		return saveContext{}, err
	}
	state.Investigation.Query.Parameters = parameters
	state.Investigation.Query.Diagnostics = state.Diagnostics
	return state, nil
}
