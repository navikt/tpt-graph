package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type stepView struct {
	Label, Status, Glyph, Detail string
}

type noticeView struct {
	Tone, Role, Glyph, Title, Body, Code string
}

type fieldView struct {
	Key, Value string
}

type resultRow struct {
	Number int
	Cells  []string
}

type runView struct {
	Source, Time, Summary, Cypher, QueryTitle, QueryCaveat string
	RawCypher                                              string
	Steps                                                  []stepView
	Notices                                                []noticeView
	Rows                                                   []resultRow
	Columns                                                []string
	Params                                                 []fieldView
	Diagnostics                                            []fieldView
	DiagOpen, CanSave                                      bool
	DiagnosticSummary                                      string
}

func durationLabel(duration time.Duration) string {
	if duration < time.Second {
		return fmt.Sprintf("%.0f ms", duration.Seconds()*1000)
	}
	return fmt.Sprintf("%.2f s", duration.Seconds())
}

func investigationView(investigation Investigation, runError, savedName string) (*runView, error) {
	view := &runView{
		Source: "Generated from question", Summary: "Nothing executed", DiagOpen: runError != "",
		Cypher: formatCypher(investigation.Query.Cypher), RawCypher: investigation.Query.Cypher, QueryTitle: "Generated query (not executed)",
		QueryCaveat: "Generated — check that it expresses your question before relying on the results.",
		Steps: []stepView{
			{Label: "Generate", Status: "idle", Glyph: "○", Detail: "not reached"},
			{Label: "Validate", Status: "idle", Glyph: "○", Detail: "not reached"},
			{Label: "Execute", Status: "idle", Glyph: "○", Detail: "not reached"},
		},
	}
	if !investigation.StartedAt.IsZero() {
		view.Time = investigation.StartedAt.Format("02 Jan 15:04:05 MST")
	}
	if savedName != "" {
		view.Source = fmt.Sprintf("Saved query %q", savedName)
		view.QueryCaveat = "Stored query, run unchanged against the current graph."
		view.Steps[0].Detail = "skipped — saved query"
	} else if investigation.Stage != "" {
		view.Steps[0] = stepView{"Generate", "done", "✓", durationLabel(investigation.GenerationDuration)}
	}
	switch investigation.Stage {
	case stageGenerate:
		view.Steps[0].Status, view.Steps[0].Glyph = "failed", "✕"
		view.Steps[0].Detail = "failed after " + durationLabel(investigation.GenerationDuration)
		if investigation.TimedOut {
			view.Steps[0].Detail = "timed out after " + durationLabel(investigation.GenerationDuration)
		}
		title := "Generation failed before producing a query."
		body := "No Cypher was produced and nothing ran against the graph. Your question is kept above — you can retry it unchanged or rephrase it. A timeout alone does not mean the question is too complex."
		if view.Cypher != "" {
			title = "Generation failed before execution."
			body = "The returned query was not executed. Your question and returned Cypher are kept for inspection."
		}
		view.Notices = append(view.Notices, noticeView{"err", "alert", "✕", title, body, runError})
	case stageValidate:
		view.Steps[1] = stepView{"Validate", "failed", "✕", "rejected"}
		view.Notices = append(view.Notices, noticeView{"err", "alert", "✕", "Query validation failed.", "Nothing ran against the graph. Inspect the query restrictions before trying again.", runError})
	case stageExecute:
		view.Steps[1] = stepView{"Validate", "done", "✓", "passed"}
		view.Steps[2] = stepView{"Execute", "done", "✓", durationLabel(investigation.ExecutionDuration)}
		view.QueryTitle = "What ran"
		view.CanSave = savedName == ""
		view.Columns = investigation.Result.Columns
		for i, row := range investigation.Result.Rows {
			view.Rows = append(view.Rows, resultRow{Number: i + 1, Cells: row})
		}
		view.Summary = fmt.Sprintf("%d rows returned", len(view.Rows))
		if runError != "" {
			view.Steps[2].Status, view.Steps[2].Glyph = "failed", "✕"
			view.Steps[2].Detail = "failed after " + durationLabel(investigation.ExecutionDuration)
			title := "Execution failed before returning any rows."
			body := "The result is unavailable; this is not a successful zero-row result."
			view.Summary = "Execution failed · no rows received"
			if len(view.Rows) > 0 {
				title = "Execution failed — the rows below are partial."
				body = fmt.Sprintf("%d rows arrived before the failure. The result is incomplete; do not read absences as meaningful.", len(view.Rows))
				view.Summary = fmt.Sprintf("%d partial rows · execution failed", len(view.Rows))
			}
			view.Notices = append(view.Notices, noticeView{"err", "alert", "✕", title, body, runError})
		} else if len(view.Rows) == 0 && !investigation.Result.Truncated {
			view.Notices = append(view.Notices, noticeView{"info", "status", "i", "This query matched nothing.", "Zero rows is not evidence that no issue exists. Check that the Cypher and parameters match your intent, and that Cartography collects the data you are asking about.", ""})
		}
		if investigation.Result.Truncated {
			if runError == "" {
				view.Summary = fmt.Sprintf("%d rows shown · output limit reached", len(view.Rows))
			}
			view.Notices = append(view.Notices, noticeView{"warn", "status", "▲", "Results are truncated and incomplete.", "A configured output limit was reached, so more matches may exist. Narrow the question, or treat counts and absences in this table as unreliable.", ""})
		}
	default:
		if runError != "" {
			view.Notices = append(view.Notices, noticeView{"err", "alert", "✕", "The investigation could not start.", "Your question is kept above.", runError})
		}
	}
	keys := make([]string, 0, len(investigation.Query.Parameters))
	for key := range investigation.Query.Parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, err := json.Marshal(investigation.Query.Parameters[key])
		if err != nil {
			return nil, fmt.Errorf("encoding query parameter %q: %w", key, err)
		}
		view.Params = append(view.Params, fieldView{"$" + key, string(value)})
	}
	generation := "not reached"
	if savedName != "" {
		generation = "not run (saved query)"
	} else if investigation.Stage != "" {
		generation = durationLabel(investigation.GenerationDuration)
	}
	execution := "not reached"
	rows := "not reached"
	if investigation.Stage == stageExecute {
		execution = durationLabel(investigation.ExecutionDuration)
		rows = fmt.Sprint(len(view.Rows))
	}
	view.Diagnostics = []fieldView{
		{"Generation duration", generation},
		{"Validation", view.Steps[1].Detail},
		{"Execution duration", execution},
		{"Rows returned/received", rows},
	}
	httpStatus, requestID, firstResponse, retries := "not reported", "not reported", "not reached", "not reported"
	if diagnostics := investigation.Query.Diagnostics; diagnostics != nil {
		if diagnostics.HTTPStatus != 0 {
			httpStatus = fmt.Sprint(diagnostics.HTTPStatus)
			firstResponse = "not reported"
		}
		if len(diagnostics.Attempts) > 0 {
			retries = fmt.Sprint(len(diagnostics.Attempts) - 1)
			attempt := diagnostics.Attempts[len(diagnostics.Attempts)-1]
			if attempt.RequestID != "" {
				requestID = attempt.RequestID
			}
			if attempt.TimeToFirstByteMS != nil {
				firstResponse = fmt.Sprintf("%.1f ms", *attempt.TimeToFirstByteMS)
			}
		}
	} else if investigation.Stage == stageExecute && savedName == "" {
		firstResponse = "not reported"
	}
	view.Diagnostics = append(view.Diagnostics, fieldView{"Time to first response", firstResponse}, fieldView{"HTTP status (model)", httpStatus}, fieldView{"Request ID", requestID}, fieldView{"Retries", retries})
	if runError != "" {
		view.Diagnostics = append(view.Diagnostics, fieldView{"Run error", runError})
	}
	view.DiagnosticSummary = "gen " + generation + " · exec " + execution
	return view, nil
}
