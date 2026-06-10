# AGENTS.md

## Purpose

Attack path mapping tool for the appsec and isoc teams at Nav. It is a read-only web interface over a Neo4j graph database populated by [Cartography](https://github.com/lyft/cartography) (CNCF). Cartography models cloud infrastructure, GitHub, Kubernetes, and Nais resources as a graph; this app queries and visualises that graph to surface ownership, dependency exposure, and attack paths.

When adding new queries or node types, research the Cartography schema first to understand what data is available and how nodes and relationships are modelled.

See `README.md` for commands, environment variables, and package layout.

## Stack

Go (1.26.3+ — `go.mod` enforces a bleeding-edge toolchain), Neo4j, standard `net/http`, `html/template`, Prometheus. No Node.js toolchain — Cytoscape.js is a vendored static file embedded at compile time. Deployed on Nais (prod-gcp, team `appsec`), Azure AD auth via Wonderwall sidecar.

## Code quality

- **Clean code is non-negotiable.** No dead code, no unused files, no speculative additions. Refactor when adding or removing features — never leave leftovers.
- **Follow Go best practices** throughout: idiomatic error handling, no global state, unexported helpers, interfaces at consumption boundaries.
- **TDD.** Write or update tests before changing behaviour. If a change to an endpoint or function does not cause a test failure, treat that as a coverage gap to fix first. Tests live alongside the package they test (`*_test.go` in the same directory).
- Tests do not require a live Neo4j instance. Use the existing interface mocks in `internal/handler/handler_test.go` as the pattern for new handler tests. Use `httptest.NewServer` for external HTTP clients.

## Git

Use `git log`, `git diff`, `git show`, and `git status` freely for read-only inspection. Commits, pushes, and anything that touches upstream are the user's responsibility — do not run them.

## CI/CD

Push to `main` (non-`.md` changes) builds the Docker image and deploys to prod-gcp automatically. There is no staging environment. All action versions in `.github/workflows/` are pinned to full SHAs.
