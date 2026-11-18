package generation

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"google.golang.org/genai"
	"tpt-graph/query-workspace/internal/modeldiag"
	"tpt-graph/query-workspace/internal/workspace"
)

func recordChatResponse(d *modeldiag.Chat, response *genai.GenerateContentResponse, err error) {
	var apiError genai.APIError
	if errors.As(err, &apiError) {
		d.HTTPStatus, d.APIStatus = apiError.Code, apiError.Status
	}
	if response != nil {
		d.ModelVersion, d.ResponseID = response.ModelVersion, response.ResponseID
		candidateCount := len(response.Candidates)
		d.CandidateCount = &candidateCount
		if len(response.Candidates) > 0 && response.Candidates[0] != nil {
			d.FinishReason = string(response.Candidates[0].FinishReason)
		}
		if response.PromptFeedback != nil {
			d.BlockReason = string(response.PromptFeedback.BlockReason)
		}
		if usage := response.UsageMetadata; usage != nil {
			d.Usage = &modeldiag.Usage{
				PromptTokens: usage.PromptTokenCount, OutputTokens: usage.CandidatesTokenCount,
				ThinkingTokens: usage.ThoughtsTokenCount, CachedTokens: usage.CachedContentTokenCount,
				ToolTokens: usage.ToolUsePromptTokenCount, TotalTokens: usage.TotalTokenCount,
			}
		}
	}
}

func chatFailure(kind, message string, cause error) error {
	return &workspace.GenerationError{Kind: kind, Message: message, Cause: cause}
}

func chatRequestFailure(err error) error {
	var apiError genai.APIError
	if errors.As(err, &apiError) {
		if apiError.Code == http.StatusGatewayTimeout || apiError.Status == "DEADLINE_EXCEEDED" {
			return chatFailure("upstream_timeout", "Google's schema assistant request timed out. Try again; a timeout does not necessarily mean the question is too complex.", err)
		}
		switch apiError.Code {
		case http.StatusTooManyRequests:
			return chatFailure("rate_limited", "Google is limiting schema assistant requests or has insufficient capacity. Wait briefly and try again.", err)
		case http.StatusInternalServerError, http.StatusServiceUnavailable:
			return chatFailure("unavailable", "Google's schema assistant service is temporarily unavailable. Try again shortly.", err)
		default:
			return chatFailure("api_error", "Google rejected the schema assistant request. Check the HTTP and API status in the diagnostics.", err)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return chatFailure("local_timeout", fmt.Sprintf("The schema assistant exceeded the application's %d-second deadline. Check the request timings in the diagnostics.", int(chatTimeout.Seconds())), err)
	}
	if errors.Is(err, context.Canceled) {
		return chatFailure("canceled", "The schema assistant request was canceled. No database query was executed.", err)
	}
	return chatFailure("schema_chat_failed", "The schema assistant could not complete its request. Check the diagnostics and try again. No database query was executed.", err)
}
