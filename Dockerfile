# Build stage — compile Go server binary
FROM golang:1.25-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# No cgo: the Opus codec (github.com/godeps/opus) runs the codec as WebAssembly
# through wazero, a pure-Go runtime, so there is no libopus to link against.
# This yields a statically linked binary and keeps libopus out of both stages.
RUN CGO_ENABLED=0 GOOS=linux go build -o /server .

# Run stage — Node.js base for TypeScript plugins; Python added for Python plugins
FROM node:22-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    python3 python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*

# Create a Python venv with the plugin SDK installed from PyPI.
# Prepend its bin/ to PATH so the server's `python3` subprocess uses it.
RUN python3 -m venv /opt/plugin-venv && \
    /opt/plugin-venv/bin/pip install --no-cache-dir streamcore-plugin
ENV PATH="/opt/plugin-venv/bin:$PATH"

# Install tsx globally so `npx tsx` works for TypeScript plugins
RUN npm install -g tsx

# Copy Go server binary
COPY --from=builder /server /server

# Copy plugins and skills
COPY plugins /plugins

# Install npm dependencies for each TypeScript plugin
RUN for dir in /plugins/plugins/*/; do \
      if [ -f "$dir/package.json" ]; then \
        echo "npm install: $dir" && \
        cd "$dir" && npm install --omit=dev; \
      fi; \
    done

# Install pip dependencies for each Python plugin (if requirements.txt exists)
RUN for dir in /plugins/plugins/*/; do \
      if [ -f "$dir/requirements.txt" ]; then \
        echo "pip install: $dir" && \
        pip install --no-cache-dir -r "$dir/requirements.txt"; \
      fi; \
    done

# Signalling (WHIP + DataChannel)
EXPOSE 8080

# Built-in STUN/TURN (internal/turn), active when server.public_ip and
# server.turn_secret are set. It binds UDP and TCP 3478 and relays media on
# UDP 50001-60000 — publish these too, or run with --network host.
EXPOSE 3478/udp
EXPOSE 3478/tcp
EXPOSE 50001-60000/udp

ENTRYPOINT ["/server"]
