FROM cgr.dev/chainguard/go:latest AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w" \
      -o /bin/tpt-graph \
      ./cmd/tpt-graph

FROM cgr.dev/chainguard/static:latest
COPY --from=builder /bin/tpt-graph /bin/tpt-graph
EXPOSE 8080
ENTRYPOINT ["/bin/tpt-graph"]
