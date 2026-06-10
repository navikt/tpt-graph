# tpt-graph

Attack path mapping tool for the appsec and isoc teams at Nav. A read-only web interface over a Neo4j graph database populated by [Cartography](https://github.com/lyft/cartography) (CNCF). Cartography models cloud infrastructure, GitHub, Kubernetes, and Nais resources as a graph; this app queries and visualises that graph to surface ownership, dependency exposure, and attack paths.

## Running locally

`NEO4J_PASSWORD` is sourced automatically from fnox. Once it is set in your shell:

```bash
make run-local
```

## Commands

```bash
make build        # go build -o bin/tpt-graph ./cmd/tpt-graph
make run          # go run ./cmd/tpt-graph
make run-local    # run with local Neo4j defaults (NEO4J_PASSWORD sourced from fnox)
make docker-build # docker build -t tpt-graph .
make clean        # rm -rf bin/
go test ./...     # run all tests
```

## Environment variables

The app exits on startup if any required variable is missing.

| Variable | Required | Description |
|---|---|---|
| `NEO4J_URI` | yes | Bolt URI, e.g. `neo4j://localhost:7687` |
| `NEO4J_USER` | yes | Neo4j username |
| `NEO4J_PASSWORD` | yes | Neo4j password |
| `WHODIS_URL` | yes | Base URL of the whodis team-ownership service |
| `PORT` | no | HTTP port, defaults to `8080` |

## Package layout

| Path | Role |
|---|---|
| `cmd/tpt-graph/main.go` | Entrypoint — wiring only |
| `internal/config/` | Env-var loading, exits on missing required vars |
| `internal/handler/` | HTTP handlers, HTML templates, static files |
| `internal/graphapi/` | Graph HTTP handlers + Cypher for graph seed/expand |
| `internal/neo4j/` | Neo4j client wrapper + all non-graph Cypher queries |
| `internal/whodis/` | HTTP client for the whodis team-ownership service |

## Contact

Reach out to the AppSec team on Slack: [#appsec](https://nav-it.slack.com/archives/C06P91VN27M)
