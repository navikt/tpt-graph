package workspace

import "tpt-graph/query-workspace/internal/modeldiag"

type GenerationDiagnostics = modeldiag.Diagnostics
type GenerationUsage = modeldiag.Usage
type HTTPAttempt = modeldiag.HTTPAttempt

type GenerationError struct {
	Kind    string
	Message string
	Cause   error
}

func (e *GenerationError) Error() string { return e.Message }
func (e *GenerationError) Unwrap() error { return e.Cause }
