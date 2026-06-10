BINARY     := bin/tpt-graph
IMAGE_NAME := tpt-graph

# NEO4J_PASSWORD is sourced automatically from fnox.
# Run `make run-local` after fnox has set it in your shell environment.

.PHONY: build run run-local docker-build clean

build:
	go build -o $(BINARY) ./cmd/tpt-graph

run:
	go run ./cmd/tpt-graph

run-local:
	NEO4J_URI=neo4j://localhost:7687 NEO4J_USER=neo4j WHODIS_URL=http://whodis go run ./cmd/tpt-graph

docker-build:
	docker build -t $(IMAGE_NAME) .

clean:
	rm -rf bin/
