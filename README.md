# TittPåGraphDataTing

Simple frontend for performing Neo4j queries.

## Environment Variables

| Variable | Description |
|---|---|
| `NEO4J_URI` | Bolt URI, e.g. `neo4j://neo4j:7687` |
| `NEO4J_USER` | Neo4j username |
| `NEO4J_PASSWORD` | Neo4j password |
| `PORT` | HTTP port (default: `8080`) |

## Running locally

```bash
NEO4J_URI=neo4j://localhost:7687 NEO4J_USER=neo4j NEO4J_PASSWORD=secret make run
```

## Contact

Reach out to the AppSec team on Slack: [#appsec](https://nav-it.slack.com/archives/C06P91VN27M)
