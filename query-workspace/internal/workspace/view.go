package workspace

import (
	"fmt"
	"strings"
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
	Nodes  string
}

type runView struct {
	Source, Time, Summary, QueryTitle, QueryCaveat string
	// Compact runs (written or saved Cypher) hide step chips and "What ran": the Cypher is in the editor.
	Compact           bool
	Executed          bool
	Steps             []stepView
	Notices           []noticeView
	Rows              []resultRow
	Columns           []string
	Graph             *graphView
	Segments          []cypherSegment
	ParamCount        int
	CopyText          string
	Diagnostics       []fieldView
	DiagOpen, CanSave bool
	DiagnosticSummary string
}

func durationLabel(duration time.Duration) string {
	if duration < time.Second {
		return fmt.Sprintf("%.0f ms", duration.Seconds()*1000)
	}
	return fmt.Sprintf("%.2f s", duration.Seconds())
}

func investigationView(investigation Investigation, runError string) (*runView, error) {
	saved := investigation.Source == SourceSaved
	written := investigation.Source == SourceWritten
	view := &runView{
		Source: "Generated from question", Summary: "Nothing executed", DiagOpen: runError != "",
		Compact: investigation.Kind == KindCypher, QueryTitle: "Generated query (not executed)",
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
	switch {
	case saved:
		view.Source = fmt.Sprintf("Saved query %q", investigation.SavedName)
		view.QueryCaveat = "Stored query, run unchanged against the current graph."
		view.Steps[0].Detail = "skipped — saved query"
	case written:
		view.Source = "Written Cypher"
	case investigation.Stage != "":
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
		if investigation.Query.Cypher != "" {
			title = "Generation failed before execution."
			body = "The returned query was not executed. Your question and returned Cypher are kept for inspection."
		}
		view.Notices = append(view.Notices, noticeView{"err", "alert", "✕", title, body, runError})
	case stageValidate:
		view.Steps[1] = stepView{"Validate", "failed", "✕", "rejected"}
		switch {
		case investigation.ParametersInvalid:
			view.Notices = append(view.Notices, noticeView{"err", "alert", "✕", "The parameters are not valid — nothing ran.",
				`Parameters must be one JSON object, for example { "language": "PHP" }. Your Cypher and parameters are kept above.`, runError})
		case view.Compact:
			body := "The workspace runs read queries only. Remove write clauses (CREATE, MERGE, SET, DELETE, REMOVE) and procedure calls (CALL), then run again."
			if written {
				body += " Your Cypher is kept above."
			}
			view.Notices = append(view.Notices, noticeView{"err", "alert", "✕", "Validation rejected this query — nothing ran.", body, runError})
		default:
			view.Notices = append(view.Notices, noticeView{"err", "alert", "✕", "Query validation failed.", "Nothing ran against the graph. Inspect the query restrictions before trying again.", runError})
		}
	case stageExecute:
		view.Executed = true
		view.Steps[1] = stepView{"Validate", "done", "✓", "passed"}
		view.Steps[2] = stepView{"Execute", "done", "✓", durationLabel(investigation.ExecutionDuration)}
		view.QueryTitle = "What ran"
		view.CanSave = !saved
		view.Columns = investigation.Result.Columns
		rowNodes := investigation.Result.Graph.RowNodes
		for i, row := range investigation.Result.Rows {
			resultRow := resultRow{Number: i + 1, Cells: row}
			if i < len(rowNodes) {
				resultRow.Nodes = strings.Join(rowNodes[i], " ")
			}
			view.Rows = append(view.Rows, resultRow)
		}
		view.Graph = newGraphView(investigation.Result.Graph)
		view.Summary = fmt.Sprintf("%d rows returned", len(view.Rows))
		if view.Compact {
			view.Summary = compactSummary(len(view.Rows), investigation)
		}
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
			view.Notices = append(view.Notices, noticeView{"err", "alert", "✕", "The investigation could not start.", "Your input is kept above.", runError})
		}
	}
	if !view.Compact && investigation.Query.Cypher != "" {
		var err error
		view.Segments, view.ParamCount, err = inlineParameters(formatCypher(investigation.Query.Cypher), investigation.Query.Parameters)
		if err != nil {
			return nil, err
		}
		runnable, _, err := inlineParameters(investigation.Query.Cypher, investigation.Query.Parameters)
		if err != nil {
			return nil, err
		}
		view.CopyText = joinSegments(runnable)
	}
	generation := "not reached"
	switch {
	case saved:
		generation = "not run (saved query)"
	case written:
		generation = "not run (written Cypher)"
	case investigation.Stage != "":
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
	} else if investigation.Stage == stageExecute && investigation.Source == SourceGenerated {
		firstResponse = "not reported"
	}
	view.Diagnostics = append(view.Diagnostics, fieldView{"Time to first response", firstResponse}, fieldView{"HTTP status (model)", httpStatus}, fieldView{"Request ID", requestID}, fieldView{"Retries", retries})
	if runError != "" {
		view.Diagnostics = append(view.Diagnostics, fieldView{"Run error", runError})
	}
	view.DiagnosticSummary = "gen " + generation + " · exec " + execution
	return view, nil
}

func compactSummary(rows int, investigation Investigation) string {
	parts := []string{fmt.Sprintf("%d %s", rows, plural(rows, "row", "rows"))}
	if graph := investigation.Result.Graph; len(graph.Nodes) > 0 || len(graph.Relationships) > 0 {
		parts = append(parts,
			fmt.Sprintf("%d %s", len(graph.Nodes), plural(len(graph.Nodes), "node", "nodes")),
			fmt.Sprintf("%d %s", len(graph.Relationships), plural(len(graph.Relationships), "relationship", "relationships")))
	}
	parts = append(parts, durationLabel(investigation.ExecutionDuration))
	return strings.Join(parts, " · ")
}
