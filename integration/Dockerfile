# Added for isolated OpenCode compatibility testing.
FROM golang:1.25.1-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY client ./client
COPY config ./config
COPY handlers ./handlers
COPY mcpbridge ./mcpbridge
COPY server ./server
COPY types ./types
RUN go vet ./... && go test ./... && CGO_ENABLED=0 go build -o /bridge ./cmd

FROM alpine:3.22.2 AS bridge
COPY --from=build /bridge /bridge
COPY LICENSE /LICENSE
USER 65532:65532
ENTRYPOINT ["/bridge"]

FROM python:3.13.7-alpine3.22 AS runner
COPY --from=build /bridge /bridge
COPY integration /tests
USER 65532:65532
ENTRYPOINT ["python", "/tests/test_mcp.py"]
