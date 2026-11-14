# Private query workspace

This is an independently buildable Go application and module. It does not
share handlers, UI, runtime configuration, or release lifecycle with
`tpt-graph`.

## Run checks

```bash
go test ./...
go build ./cmd/query-workspace
```

## Investigation workbench

The UI is server-rendered with `html/template`. Templates, one stylesheet, one
progressive-enhancement script, a vendored Cytoscape.js (MIT) and self-hosted IBM
Plex fonts are embedded in the Go binary. There is no frontend framework, build
pipeline or external asset request. Font licences are included alongside the
static files; the Cytoscape.js licence is kept in its file header.

The composer has two modes. **Ask a question** generates Cypher with Vertex AI,
then validates and runs it immediately (`POST /ask`). **Write Cypher** runs
hand-written Cypher with optional JSON parameters (`POST /run`) and needs no
schema snapshot or model. Both paths use the same read-only validation as
generated queries; `CALL`, writes and block comments are rejected before
execution. Each mode keeps its own draft and last result. Switching mode
(`POST /mode`) stores the current draft. **What ran** shows the executed Cypher
with parameter values inline. In ask mode, **Open in Write Cypher**
(`POST /runs/{id}/open`) copies that query and its parameters into the Cypher
draft without running it.

Results returning nodes or relationships can also be drawn as a graph. A
Table/Graph/Both toggle (JavaScript only) remembers its choice per tab. The graph
draws at most 300 nodes and 2,000 relationships, and only relationships whose
endpoints are both returned; nothing extra is fetched. Every node is also listed
as a button, so selection works by keyboard, and selecting a node highlights its
rows in the table. Without JavaScript the table remains available.

Saved queries live in one shared catalogue of nested folders (`Team > GitHub`).
New folders default to `Drafts`. A saved query is either a question (rerun skips
generation) or written Cypher (rerun opens the Write Cypher tab with a compact
run strip). Search matches names, folder paths and questions, using client-side
filtering when JavaScript is available and `GET /?q=...` otherwise. Folder
collapse state is kept in `localStorage`, and the sidebar can be hidden
(`POST /sidebar`, a cookie).

Run state is held in an in-memory session (`query_workspace_session` cookie,
HttpOnly). Sessions expire after 60 minutes idle, and all sessions together are
capped at 64 MiB: expired sessions are evicted first, then the least recently
used. Restarts lose sessions; the UI then says the session ended and reruns
nothing. Saving (`POST /save`) stores the last run of the current mode by run ID,
even when the draft has since changed (a stale notice is shown); with JavaScript the
edited draft is kept. A run that is no
longer current is rejected, and the user must run the query again. Results and
generation diagnostics are not stored in the catalogue.

The saved-folders migration (`20261104000000_saved_query_folders.sql`) is
destructive: it drops and recreates `saved_queries`, so existing saved queries
are lost.

Zero rows, incomplete output, partial execution failures and generation failures
have distinct notices. Schema freshness describes metadata, not graph-data
freshness. Truncation copy refers to an output limit rather than assuming a row
limit: collection can stop at the row or byte cap. Catalogue failures do not hide
investigation results or masquerade as an empty catalogue, but do hide the save
form.

Without JavaScript, forms, catalogue search, mode switching and native disclosure
controls still work. JavaScript adds Cmd/Ctrl+Enter submission, Copy Cypher,
instant filtering, folder collapse, the result view toggle, the graph and the
pending state. Keyboard focus is visible; errors are associated with invalid
fields, and status messages do not rely on colour alone.

## Schema chat

Open **Schema chat** (`GET /schema`) to explore what the cached schema contains.
The assistant uses its own Vertex AI model setting, with two bounded tools:
`search_schema` matches substrings in labels, property names and relationship
types, and `inspect_node` retrieves an exact label and its directed one-hop
schema neighbourhood. It receives retrieved metadata rather than the full
schema. It has no Neo4j query tool and cannot retrieve records or property values.

Each answer requires a successful metadata lookup, even for follow-ups. A turn
has a 45-second deadline, at most six tool calls and seven model requests.
Each tool result is limited to 24 types, 40 inferred relationships, 60 properties
per type and 32 KiB encoded metadata. Truncation is explicit; narrower searches
can focus the result. The latest answer's retrieved evidence is shown as a
focused graph, property/type cards and a directed relationship list. The cards
and relationship list also work without JavaScript.

The assistant must distinguish metadata from business meaning and coverage:
a branch type does not establish that all branches are ingested, and an inferred
relationship does not prove that an actual edge exists. Absence means "not found
in this snapshot", not that the source system lacks the information. Answers
show the snapshot capture time. Use the query workspace to inspect actual records.

`POST /schema/chat` keeps the last eight exchanges in the existing browser
session, under the same idle expiry and memory budget as investigation results.
Chat is not saved in PostgreSQL. `POST /schema/reset` clears the conversation;
in-flight answers cannot restore a conversation that was reset or overwrite a
newer answer. Failed turns retain the question for retry without changing history.

## Schema-only model access

**Models must NEVER have access to database data, only schema metadata.**
This boundary applies to query generation and chat, regardless of the model
or provider selected. Schema metadata means labels, property names and types,
relationship types, and metadata-inferred relationship patterns. It does not
include actual records, property values, record counts, query results, or
verified graph edges.

The application enforces this separation:

- Generation receives the user's question and the cached schema. It has no tools.
  The application validates and executes the generated Cypher separately; results,
  result graphs, and raw execution errors are never sent back to the model.
- Chat receives the user's question, earlier schema-chat questions and answers,
  and bounded metadata lookups. Its only tools are `search_schema` and
  `inspect_node`; neither can query database records. Unknown tools are rejected.
  Investigation results and hand-written Cypher drafts are not chat context.
- Schema discovery projects only metadata from the three fixed native schema
  procedures. It does not include sample records or property values. Failure
  never falls back to sampling data or giving the model database access.

Do not add model-driven data queries, result summarization, execution-error
feedback for query repair, or data attachments to prompts, tools, or history.
Higher reasoning effort or a model change must not weaken this boundary.
The application still needs Neo4j access to run user investigations; the model
does not receive that executor or its credentials.

**Free-text input is not automatically sanitized.** Questions and schema-chat
history are sent to Vertex AI, so users must not paste graph records, query
results, credentials, or other secrets into either question form. Both forms
display this warning. Model instructions also prohibit requesting records,
query results, credentials, or database access.

## Runtime configuration

Nais provides `DB_URL` for the PostgreSQL catalogue. The `tpt-query-workspace`
secret must provide:

| Variable | Purpose |
|---|---|
| `NEO4J_URI` | Neo4j Bolt URI |
| `NEO4J_USER` | Neo4j user with access to the target database and native schema procedures |
| `NEO4J_PASSWORD` | Password for that user |
| `GOOGLE_CLOUD_PROJECT` | Vertex AI project |

Application access is controlled by the Nais manifest and login sidecar.
`GOOGLE_CLOUD_LOCATION`, `VERTEX_MODEL`, and `VERTEX_CHAT_MODEL` are set in
the Nais manifest.
The `eu` location uses `aiplatform.eu.rep.googleapis.com`, which must be
allowed in `accessPolicy.outbound.external`.
Local development also needs the same values plus a working Application
Default Credentials identity.

Query generation and chat use independent model settings:

| Workload | Model variable | Default | Thinking | Maximum output tokens per request |
|---|---|---|---|---|
| Query generation | `VERTEX_MODEL` | `gemini-3.8-flash` | LOW | 4,096 |
| Schema chat | `VERTEX_CHAT_MODEL` | `gemini-3.8-flash` | HIGH | 16,384 |

Changing `VERTEX_MODEL` does not change the chat model. Both workloads retain
the 45-second deadline; for chat this covers all model and tool rounds, not
each request. Chat has a separate, larger token budget to accommodate reasoning
and its final answer; displayed answers remain limited to 16,000 bytes.
HIGH thinking prioritizes reasoning quality, not lower latency, and can increase
token use and cost. It does not guarantee a better answer or fewer requests.
No live-model quality or latency comparison is established by the automated
tests, which use synthetic metadata and a local fake provider.

Before changing models, confirm support for the configured EU endpoint, thinking
level, function calling, and token budget. Do not silently fall back to a global
endpoint, another provider, or a weaker model. Compare configurations using
schema-only questions and metadata, measuring grounded correctness, appropriate
uncertainty, tool rounds, token use, timeouts, and end-to-end latency.

The defaults for `NEO4J_DATABASE`, query timeout, row cap, and result byte cap
are configurable with `NEO4J_DATABASE`, `NEO4J_QUERY_TIMEOUT_SECONDS`,
`NEO4J_MAX_RESULT_ROWS`, and `NEO4J_MAX_RESULT_BYTES`. These application limits
bound request time and collected output; only Neo4j-side least-privilege
credentials provide the database write boundary, and output limits do not
guarantee bounded server-side computation.

Queries use parameterized read-only Cypher without procedure calls or block
comments. JSON integer parameters retain signed 64-bit precision when generated,
saved, and rerun; out-of-range integers are rejected.

The experimental deployment can reuse tpt-graph's Neo4j credentials. If that
identity can write, query checks and read routing do not provide a
database-enforced read-only guarantee.

Confirm application access rules, model project/IAM access, Neo4j credentials,
ingress, and Cloud SQL tier before deployment. A Nais outbound rule provides
network access only.

## Daily schema cache

The single application replica refreshes the Neo4j schema at **00:00
Europe/Oslo** every day, following daylight-saving changes. Timezone data is
embedded in the binary so this also works in the static container. Startup
captures a snapshot in the background if none exists for the configured target
or the last capture predates today's local midnight. No separate scheduled
Nais job is needed.

Discovery calls `db.schema.nodeTypeProperties()`,
`db.schema.relTypeProperties()`, and `db.schema.visualization()` against
`NEO4J_DATABASE`. APOC is not required. The discovery budget is two minutes;
these metadata procedures can scan the graph. Collected metadata and the
resulting schema text are size-limited. Empty, oversized, incomplete due to
errors, or failed discoveries do not replace the last successful snapshot.

PostgreSQL retains successful snapshots with their capture time, discovery
method, and SHA-256 hash. Snapshots are scoped by a fingerprint of the Neo4j
URI and database name. Each generation request reads the latest snapshot from
PostgreSQL and includes it in the Vertex AI prompt; it does not repeat discovery
or train the model.

The UI shows snapshot age, staleness, and refresh failures. A snapshot is stale
when it predates today's Oslo midnight, but it remains usable if refresh fails.
The next attempt is at the following midnight or on restart if overdue. Without
a snapshot, generation is disabled and returns an explicit error; saved queries
can still be listed and run. Refresh shuts down before database clients close.

Snapshots contain metadata rather than node property values. Relationship
patterns from `db.schema.visualization()` are explicitly marked as inferred:
they may not occur in actual data and do not establish business meaning or
prove an attack path.

## Tracing

Nais SDK-only instrumentation supplies the OpenTelemetry collector endpoint,
service identity, resource attributes, and sampling configuration. The application
exports traces over OTLP/gRPC and flushes pending spans during shutdown.
Without an OTLP endpoint, export is disabled for local development.

Request traces include Vertex AI generation and HTTP calls, Neo4j execution,
PostgreSQL catalogue and schema operations, and background schema refreshes.
Health checks and metrics requests are not traced. Spans contain operation
metadata and error classifications, not investigation questions, Cypher,
parameters, results, or raw application error messages. Context-aware JSON logs
include `trace_id` and `span_id` for correlation.

Open [Nais APM](https://grafana.nav.cloud.nais.io/a/nais-apm-app) and select
`tpt-query-workspace`, or use the Tempo data source in Grafana Explore:

```traceql
{ resource.service.name = "tpt-query-workspace" }
```

An investigation can return an HTTP 200 page containing an error; its request
span is still marked as failed.

## Generation diagnostics

Every generation attempt displays a diagnostic JSON panel, including failures
that produce no query. It includes model/location/settings, prompt version,
question and system-instruction byte counts, schema size/hash/capture time,
elapsed time, trace ID, HTTP/API status, and response metadata when available.
Response metadata includes model version, response ID, finish/block reasons,
candidate count, and prompt/output/thinking/cached/tool/total token counts.

Each outbound attempt records connection reuse, DNS, TCP and TLS durations,
elapsed time to connection readiness, request completion and first response
byte, time waiting after the request was written, total HTTP duration, and
selected request-ID headers. All numeric timings are milliseconds. Connection
setup phases can be absent on reused connections. Missing usage or response
fields mean unavailable, not zero. Waiting for the first response includes
provider processing and network latency; it cannot distinguish Google's
queueing from model computation.

The same metadata is recorded in the structured `query generation completed`
log under `generation`; key outcome/model/token fields also appear on the
generation span. Prompts, questions, schema text, raw response bodies, headers
other than request IDs, and generated queries are not included in diagnostics.
Diagnostics are not persisted in saved queries.

Google deadlines and local deadlines have distinct outcomes. Output-limit,
blocked, incomplete and malformed responses are not executed. A deadline alone
does not establish that a question is too complex. Request settings, prompts,
retry behavior and the 45-second generation budget are unchanged so the added
measurements can be compared with the existing baseline.

## Optional integration checks

Ordinary tests do not need external services. Set these only for disposable
test databases:

```bash
QUERY_WORKSPACE_TEST_DATABASE_URL=postgres://... go test ./internal/catalog -count=1
QUERY_WORKSPACE_TEST_NEO4J_URI=bolt://localhost:7687 \
  QUERY_WORKSPACE_TEST_NEO4J_PASSWORD=... go test ./internal/queryengine -count=1
```

The Neo4j integration test uses the `neo4j` user and database and creates and
removes synthetic fixture nodes. Never point it at a shared database.
