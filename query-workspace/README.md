# Private query workspace

This is an independently buildable Go application and module. It does not
share handlers, UI, runtime configuration, or release lifecycle with
`tpt-graph`.

## Run checks

```bash
go test ./...
go build ./cmd/query-workspace
```

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
`GOOGLE_CLOUD_LOCATION` and `VERTEX_MODEL` are set in the Nais manifest.
The `eu` location uses `aiplatform.eu.rep.googleapis.com`, which must be
allowed in `accessPolicy.outbound.external`.
Local development also needs the same values plus a working Application
Default Credentials identity.

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
