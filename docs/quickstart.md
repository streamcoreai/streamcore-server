# Quick start

Everything needed to get a realtime voice session running locally, plus the
Docker and fully-local variants.

## Prerequisites

For Docker: Docker. There is no Compose file for local development — Compose is used only by the EC2 deployment in [`infrastructure/aws/ec2`](../infrastructure/aws/ec2/).

For local development:

- Go 1.25+ — matches the `go` directive in [`go.mod`](../go.mod); CI and the Docker build use the same toolchain
- Node.js 20+ and npm
- Python 3.10+ for Python plugins or examples
- Rust 1.87+ for Rust SDKs or examples

## Run the media runtime

```bash
cp config.toml.example config.toml
# Edit config.toml with your provider credentials

go run .
```

Or with Docker:

```bash
docker build -t streamcore-server .
docker run --rm -p 8080:8080 -v "$(pwd)/config.toml:/config.toml:ro" streamcore-server
```

The server listens on `:8080`. Clients connect to `http://localhost:8080/whip`.

That publishes the signalling port only, which is all a local browser client needs. If you set `server.public_ip` and `server.turn_secret`, the built-in STUN/TURN server ([`internal/turn`](../internal/turn/)) also listens on UDP **and** TCP 3478 and relays media on UDP 50001–60000, so those have to be reachable as well:

```bash
docker run --rm \
  -p 8080:8080 \
  -p 3478:3478/udp -p 3478:3478/tcp \
  -p 50001-60000:50001-60000/udp \
  -v "$(pwd)/config.toml:/config.toml:ro" \
  streamcore-server
```

On Linux, prefer `--network host` over publishing that range: Docker starts a userland proxy per published port, so a 10,000-port UDP range makes container startup slow and adds a hop to every relayed packet. That is why the EC2 deployment uses `network_mode: host`.

## Connect a client

```bash
git clone https://github.com/streamcoreai/examples.git
cd examples/typescript
npm install
npm run dev
```

Open [http://localhost:3000](http://localhost:3000). It connects to `http://localhost:8080/whip` by default.

## Connect your own backend

The point of StreamCore is that the intelligence is yours. The fastest path is a tool that calls your service — the agent keeps talking while your backend does the work.

```bash
mkdir -p plugins/plugins/orders-lookup
```

`plugins/plugins/orders-lookup/plugin.yaml`

```yaml
name: orders.lookup
description: Look up an order by ID in the company order system
version: 1
language: python
entrypoint: main.py
thinking_sound: true
parameters:
  type: object
  properties:
    order_id:
      type: string
      description: The customer's order ID
  required:
    - order_id
```

`plugins/plugins/orders-lookup/main.py`

```python
import os, requests
from streamcoreai_plugin import StreamCoreAIPlugin

plugin = StreamCoreAIPlugin()

@plugin.on_execute
def handle(params):
    r = requests.get(
        f"{os.environ['BACKEND_URL']}/orders/{params['order_id']}",
        timeout=10,
    )
    r.raise_for_status()
    order = r.json()
    return f"Order {order['id']} is {order['status']}, arriving {order['eta']}."

plugin.run()
```

Restart the server. Your backend is now part of a realtime voice session, and StreamCore handled every millisecond of the media path around it.

To own the whole conversation rather than one tool call, see [Bring your own agent](./bring-your-own-agent.md).

## Fully local (no API keys)

Run everything on your own hardware with Ollama for the LLM and VibeVoice for STT/TTS.

**1. Install and start Ollama**

```bash
brew install ollama            # macOS; see https://ollama.ai for Linux
ollama serve
ollama pull gpt-oss:20b
```

**2. Start the VibeVoice sidecars**

```bash
# Apple Silicon (MLX)
pip install mlx-audio numpy websockets fastapi uvicorn
# OR Linux / CUDA
# pip install torch transformers librosa numpy websockets fastapi uvicorn

python external/vibeVoice/vibeVoiceAsr/server.py   # ws://127.0.0.1:8200
python external/vibeVoice/vibeVoiceTTS/server.py   # http://127.0.0.1:8300
```

**3. Configure and run**

```toml
[stt]
provider = "vibevoice"

[llm]
provider = "ollama"

[tts]
provider = "vibevoice"

[ollama]
base_url = "http://localhost:11434"
model = "gpt-oss:20b"

[vibevoice]
asr_url = "ws://127.0.0.1:8200"
tts_url = "http://127.0.0.1:8300"
voice = "en-Emma_woman"
```

```bash
go run .
```

Fully local realtime voice, no external API dependencies. Model and sidecar details in [Providers → Local VibeVoice setup](./providers.md#local-vibevoice-setup).

---

Next: [Configuration reference](./configuration.md) · [Providers](./providers.md) · [Agent runtime](./agent-runtime.md)
