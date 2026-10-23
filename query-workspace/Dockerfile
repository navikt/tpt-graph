FROM cgr.dev/chainguard/go:latest AS builder
ENV GOOS=linux
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /bin/query-workspace ./cmd/query-workspace

FROM cgr.dev/chainguard/static:latest
WORKDIR /app
COPY --from=builder /bin/query-workspace /app/query-workspace
ENTRYPOINT ["/app/query-workspace"]
