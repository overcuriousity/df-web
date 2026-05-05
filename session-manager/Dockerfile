FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o session-manager .

FROM debian:stable-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates novnc \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /src/session-manager /app/session-manager
ENTRYPOINT ["/app/session-manager"]
