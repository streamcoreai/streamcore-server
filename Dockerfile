# Build stage
FROM golang:1.22-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    libopus-dev pkg-config \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /server .
RUN CGO_ENABLED=1 GOOS=linux go build -o /server-standard ./cmd/standard
RUN CGO_ENABLED=1 GOOS=linux go build -o /server-mediasoup ./cmd/mediasoup

# Run stage
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates libopus0 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /server /server
COPY --from=builder /server-standard /server-standard
COPY --from=builder /server-mediasoup /server-mediasoup

EXPOSE 8080

ENTRYPOINT ["/server"]
