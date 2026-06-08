BINARY     := bin/tpt-graph
IMAGE_NAME := tpt-graph

.PHONY: build run docker-build clean

build:
	go build -o $(BINARY) ./cmd/tpt-graph

run:
	go run ./cmd/tpt-graph

docker-build:
	docker build -t $(IMAGE_NAME) .

clean:
	rm -rf bin/
