package workspace

import "time"

type GenerationDiagnostics struct {
	TraceID         string           `json:"trace_id,omitempty"`
	Model           string           `json:"model"`
	Location        string           `json:"location"`
	PromptVersion   string           `json:"prompt_version"`
	ThinkingLevel   string           `json:"thinking_level"`
	MaxOutputTokens int32            `json:"max_output_tokens"`
	TimeoutSeconds  int              `json:"timeout_seconds"`
	QuestionBytes   int              `json:"question_bytes"`
	SystemBytes     int              `json:"system_instruction_bytes"`
	SchemaBytes     int              `json:"schema_bytes"`
	SchemaHash      string           `json:"schema_sha256,omitempty"`
	SchemaCaptured  *time.Time       `json:"schema_captured_at,omitempty"`
	DurationMS      float64          `json:"duration_ms"`
	Outcome         string           `json:"outcome"`
	HTTPStatus      int              `json:"http_status,omitempty"`
	APIStatus       string           `json:"api_status,omitempty"`
	ModelVersion    string           `json:"model_version,omitempty"`
	ResponseID      string           `json:"response_id,omitempty"`
	CandidateCount  *int             `json:"candidate_count,omitempty"`
	FinishReason    string           `json:"finish_reason,omitempty"`
	BlockReason     string           `json:"block_reason,omitempty"`
	Usage           *GenerationUsage `json:"usage,omitempty"`
	Attempts        []HTTPAttempt    `json:"http_attempts"`
}

type GenerationUsage struct {
	PromptTokens   int32 `json:"prompt_tokens"`
	OutputTokens   int32 `json:"output_tokens"`
	ThinkingTokens int32 `json:"thinking_tokens"`
	CachedTokens   int32 `json:"cached_tokens"`
	ToolTokens     int32 `json:"tool_tokens"`
	TotalTokens    int32 `json:"total_tokens"`
}

type HTTPAttempt struct {
	HTTPStatus        int      `json:"http_status,omitempty"`
	RequestID         string   `json:"request_id,omitempty"`
	DurationMS        float64  `json:"duration_ms"`
	ConnectionReused  *bool    `json:"connection_reused,omitempty"`
	DNSMS             *float64 `json:"dns_ms,omitempty"`
	ConnectMS         *float64 `json:"connect_ms,omitempty"`
	TLSMS             *float64 `json:"tls_ms,omitempty"`
	ConnectionReadyMS *float64 `json:"connection_ready_ms,omitempty"`
	RequestWriteMS    *float64 `json:"request_written_ms,omitempty"`
	TimeToFirstByteMS *float64 `json:"time_to_first_byte_ms,omitempty"`
	WaitAfterWriteMS  *float64 `json:"wait_after_write_ms,omitempty"`
	ErrorType         string   `json:"error_type,omitempty"`
}

type GenerationError struct {
	Kind    string
	Message string
	Cause   error
}

func (e *GenerationError) Error() string { return e.Message }
func (e *GenerationError) Unwrap() error { return e.Cause }
