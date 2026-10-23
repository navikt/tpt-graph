BINARY     := bin/tpt-graph
IMAGE_NAME := tpt-graph

# NEO4J_PASSWORD is sourced automatically from fnox.
# Run `make run-local` after fnox has set it in your shell environment.

.PHONY: build run run-local docker-build clean query-workspace-build query-workspace-test query-workspace-docker-build

build:
	go build -o $(BINARY) ./cmd/tpt-graph

run:
	go run ./cmd/tpt-graph

run-local:
	NEO4J_URI=neo4j://localhost:7687 NEO4J_USER=neo4j WHODIS_URL=http://whodis go run ./cmd/tpt-graph

docker-build:
	docker build -t $(IMAGE_NAME) .

query-workspace-build:
	mkdir -p bin
	cd query-workspace && go build -o ../bin/query-workspace ./cmd/query-workspace

query-workspace-test:
	cd query-workspace && go test ./...

query-workspace-docker-build:
	docker build -f query-workspace/Dockerfile -t tpt-query-workspace query-workspace

clean:
	rm -rf bin/
