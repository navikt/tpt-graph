package generation

import (
	"context"
	"errors"
	"net/http"

	"google.golang.org/genai"
	"tpt-graph/query-workspace/internal/modeldiag"
	"tpt-graph/query-workspace/internal/workspace"
)

func recordChatResponse(d *modeldiag.Chat, request *modeldiag.ChatRequest, response *genai.GenerateContentResponse, err error) {
	if len(request.Attempts) > 0 {
		request.HTTPStatus = request.Attempts[len(request.Attempts)-1].HTTPStatus
	}
	var apiError genai.APIError
	if errors.As(err, &apiError) {
		request.HTTPStatus, request.APIStatus = apiError.Code, apiError.Status
	}
	if response != nil {
		request.ModelVersion, request.ResponseID = response.ModelVersion, response.ResponseID
		candidateCount := len(response.Candidates)
		request.CandidateCount = &candidateCount
		if len(response.Candidates) > 0 && response.Candidates[0] != nil {
			request.FinishReason = string(response.Candidates[0].FinishReason)
		}
		if response.PromptFeedback != nil {
			request.BlockReason = string(response.PromptFeedback.BlockReason)
		}
		if usage := response.UsageMetadata; usage != nil {
			request.Usage = &modeldiag.Usage{
				PromptTokens: usage.PromptTokenCount, OutputTokens: usage.CandidatesTokenCount,
				ThinkingTokens: usage.ThoughtsTokenCount, CachedTokens: usage.CachedContentTokenCount,
				ToolTokens: usage.ToolUsePromptTokenCount, TotalTokens: usage.TotalTokenCount,
			}
			if d.Usage == nil {
				d.Usage = &modeldiag.Usage{}
			}
			d.Usage.PromptTokens += request.Usage.PromptTokens
			d.Usage.OutputTokens += request.Usage.OutputTokens
			d.Usage.ThinkingTokens += request.Usage.ThinkingTokens
			d.Usage.CachedTokens += request.Usage.CachedTokens
			d.Usage.ToolTokens += request.Usage.ToolTokens
			d.Usage.TotalTokens += request.Usage.TotalTokens
			d.UsageRequests++
		}
	}
	d.HTTPStatus, d.APIStatus = request.HTTPStatus, request.APIStatus
	d.ModelVersion, d.ResponseID = request.ModelVersion, request.ResponseID
	d.CandidateCount, d.FinishReason, d.BlockReason = request.CandidateCount, request.FinishReason, request.BlockReason
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
		return chatFailure("local_timeout", "The schema assistant exceeded the application's 45-second deadline. Check the request timings in the diagnostics.", err)
	}
	if errors.Is(err, context.Canceled) {
		return chatFailure("canceled", "The schema assistant request was canceled. No database query was executed.", err)
	}
	return chatFailure("schema_chat_failed", "The schema assistant could not complete its request. Check the diagnostics and try again. No database query was executed.", err)
}
