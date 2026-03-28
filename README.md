# Voice Agent

A real-time AI voice agent built with WebRTC. Speak naturally in your browser—the agent transcribes your speech, runs it through an LLM, and speaks back with low-latency TTS, all over a peer-to-peer audio connection. Signaling follows the [WHIP standard (RFC 9725)](https://www.rfc-editor.org/rfc/rfc9725.html) — a single HTTP POST exchanges the SDP offer/answer with no persistent signaling connection required.

This directory is one component of a multi-package layout. For mapping it to separate Git repositories and published SDK versions, see [Repository layout](../docs/repository-structure.md).

## Features

- **Real-time voice conversation** — WebRTC audio streaming with sub-100ms round-trip
- **Configurable TTS** — [Cartesia Sonic](https://cartesia.ai/sonic) (default) or [Deepgram Aura](https://deepgram.com) for text-to-speech
- **Streaming STT** — [Deepgram](https://deepgram.com) real-time transcription with built-in VAD
- **LLM-powered** — [OpenAI GPT-4o-mini](https://platform.openai.com) with conversation history
- **Plugin system** — Extend the agent with Python, TypeScript/JS, or Go native plugins via [OpenAI function calling](https://platform.openai.com/docs/guides/function-calling)
- **Skills** — Markdown-based behavioral instructions that shape the agent's personality and responses
- **Session-based** — Each connection gets a unique session ID (UUID); no shared rooms
- **Open source** — Apache 2.0 licensed, Go + Next.js stack
- **Client SDKs** — Framework-agnostic [TypeScript](typescript-sdk/), [Go](golang-sdk/), [Rust](rust-sdk/), and [Python](python-sdk/) SDKs
- **Plugin SDKs** — [Python](plugin-sdk/python/) and [TypeScript](plugin-sdk/typescript/) SDKs for building plugins

## In Function Now

| Component | Status | Notes |
|-----------|--------|-------|
| WebRTC audio (Pion) | ✅ | Opus RTP bidirectional streaming |
| WHIP signaling | ✅ | Single HTTP POST, full ICE gathering |
| DataChannel events | ✅ | Transcript & response messages over DC |
| Session management | ✅ | Auto-generated UUID per session |
| Deepgram STT | ✅ | Streaming WebSocket, Nova-2, endpointing |
| OpenAI LLM | ✅ | GPT-4o-mini, streaming, conversation history |
| Cartesia TTS | ✅ | Sonic-3, Katie voice, pcm_s16le |
| Deepgram TTS | ✅ | Aura Asteria, switchable via env |
| Next.js client | ✅ | One-click connect, live transcript, audio visualizer |
| Plugin system | ✅ | Python, TypeScript/JS, Go native plugins |
| Skills system | ✅ | Markdown-based behavioral instructions |
| OpenAI tool calling | ✅ | Streaming function calling with tool loop |

## TODO

- [x] **Tool calling** — LLM function/tool execution via plugin system
- [ ] **RAG** — Retrieval-augmented generation (documents, knowledge base)
- [ ] **Memory** — Persistent conversation memory across sessions
- [ ] **Sip connection** connect to the agent with sip client
- [x] **Client SDK** — TypeScript, Go, Rust, and Python SDKs in `typescript-sdk/`, `golang-sdk/`, `rust-sdk/`, and `python-sdk/`
- [x] **Plugin system** — Extensible plugin/skills architecture with Python and TypeScript SDKs

## Architecture

```
┌─────────────────────┐                    ┌─────────────────────────────────────┐
│   Next.js Client    │                    │           Go Server (Pion)           │
│                     │                    │                                       │
│  Mic → WebRTC ──────┼──── Opus RTP ──────┼──→ Opus Decode → Deepgram STT        │
│  Speaker ← WebRTC ←─┼──── Opus RTP ←─────┼──← Opus Encode ← TTS (Cartesia/DG)    │
│                     │                    │         │                            │
│  HTTP POST ─────────┼── WHIP (SDP) ──────┼──→ Peer created, answer returned       │
│  DataChannel ◄──────┼──── transcript ←───┼──← OpenAI GPT (streaming)              │
│                     │                    │                                       │
└─────────────────────┘                    └─────────────────────────────────────┘
```

**Signaling:** Client creates a DataChannel + SDP offer (with all ICE candidates gathered), POSTs it to `/whip`, and receives the SDP answer with a server-generated session ID (UUID). No persistent signaling connection needed.

**Pipeline flow:** Browser captures mic → Opus over WebRTC → Server decodes to PCM → Deepgram STT → OpenAI LLM (with tool calling) → Cartesia/Deepgram TTS → Opus over WebRTC → Browser plays audio. Transcript and response text are delivered back to the client via a WebRTC DataChannel.

**Plugins & Skills:** The LLM can invoke external tools (Python/TypeScript plugins) via function calling. Skills inject behavioral instructions into the system prompt. See the [Plugin Guide](docs/plugins.md) and [Skills Guide](docs/skills.md) for details.

## Prerequisites

**For Docker:** Docker and Docker Compose

**For local development:**
- **Go 1.22+**
- **Node.js 18+** and npm
- **Rust 1.87+** (for building the Rust SDK or example)
- **Python 3.10+** (for building the Python SDK or example)

**API keys (required):**
  - [Deepgram](https://deepgram.com) — required for STT
  - [OpenAI](https://platform.openai.com) — required for LLM
  - [Cartesia](https://cartesia.ai) — for TTS (default), or use Deepgram for TTS

## Quick Start

### Option A: Docker Compose (recommended)

**1. Configure the server**

```bash
cp server/config.toml.example server/config.toml
# Edit server/config.toml with your API keys
```

**2. Build and run**

```bash
docker compose up --build
```

**3. Open [http://localhost:3000](http://localhost:3000)** — click Connect.

> **Note:** The client connects to `http://localhost:8080/whip` by default. If you access the app via a different host (e.g. `http://192.168.1.x:3000`), rebuild the client with:
> ```bash
> docker compose build --build-arg NEXT_PUBLIC_WHIP_URL=http://YOUR_HOST:8080/whip client
> docker compose up
> ```

### Option B: Local development

**1. Clone and configure the server**

```bash
cd server
cp config.toml.example config.toml
# Edit config.toml with your API keys
```

**2. Start the server**

```bash
go run main.go
```

**3. Start the client**

```bash
npm install          # from project root (uses workspaces)
cd examples/typescript
npm run dev
```

**4. Open [http://localhost:3000](http://localhost:3000)** — click Connect.

## Configuration

### Server (`server/config.toml`)

```toml
[server]
port = "8080"

[pipeline]
barge_in = true           # Allow user to interrupt agent mid-speech (default: true)

[stt]
provider = "deepgram"     # Supported: deepgram, openai

[llm]
provider = "openai"       # Supported: openai

[tts]
provider = "cartesia"     # Supported: cartesia, deepgram, elevenlabs

[deepgram]
api_key = ""              # Required — used by STT (and TTS if selected)

[openai]
api_key = ""              # Required — used by LLM
model = "gpt-4o-mini"     # Model name (default: gpt-4o-mini)
system_prompt = "..."     # Optional — defaults to helpful assistant

[cartesia]
api_key = ""              # Required if tts.provider = "cartesia"
voice_id = ""             # Optional — Cartesia voice ID (default: Katie)

[elevenlabs]
api_key = ""              # Required if tts.provider = "elevenlabs"
voice_id = ""             # Optional — ElevenLabs voice ID (default: Rachel)
model = ""                # Optional — model (default: eleven_turbo_v2_5)

[plugins]
directory = "./plugins"   # Path to plugin/skills directory (default: ./plugins)
```

To switch providers, change the `provider` field in `[stt]`, `[llm]`, or `[tts]` and add the matching provider section with credentials.

## Plugins & Skills

The agent supports a plugin system for extending functionality and a skills system for shaping behavior. Plugins give the LLM the ability to call external tools during conversation. Skills inject instructions into the system prompt.

- **[Plugin Guide](docs/plugins.md)** — How to build plugins in Python, TypeScript/JS, or Go
- **[Skills Guide](docs/skills.md)** — How to create skills that shape agent behavior
- **[Architecture & Protocol](plugins-skills-system.md)** — Full system design document

### Quick Example

Create a Python plugin that tells the time:

```bash
mkdir -p server/plugins/plugins/time-get
```

**`plugin.yaml`**
```yaml
name: time.get
description: Get the current time in a timezone
version: 1
language: python
entrypoint: main.py
parameters:
  type: object
  properties:
    timezone:
      type: string
      description: IANA timezone name
  required:
    - timezone
```

**`main.py`**
```python
from datetime import datetime
from zoneinfo import ZoneInfo
from streamcoreai_plugin import StreamCoreAIPlugin

plugin = StreamCoreAIPlugin()

@plugin.on_execute
def handle(params):
    tz = ZoneInfo(params["timezone"])
    now = datetime.now(tz)
    return f"The current time is {now.strftime('%I:%M %p')} in {params['timezone']}."

plugin.run()
```

Restart the server — the agent can now tell users the time when asked.

### Client

The TypeScript example at `examples/typescript/` uses `@streamcoreai/js-sdk` to connect. The WHIP endpoint defaults to `http://localhost:8080/whip` and can be configured via the SDK's `StreamCoreAIConfig`.

## Signaling Protocol (WHIP — RFC 9725)

Signaling follows [RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html) (WebRTC-HTTP Ingestion Protocol).

### HTTP — SDP Exchange

| Step | Method | Path | Body | Response |
|------|--------|------|------|----------|
| 1 | `POST` | `/whip` | SDP offer (`application/sdp`) | `201 Created` with SDP answer, `Location: /whip/{sessionId}`, `ETag` header |
| 2 | `DELETE` | `/whip/{sessionId}` | — | `200 OK` (session terminated) |
| — | `OPTIONS` | `/whip/*` | — | `204` with `Accept-Post: application/sdp` |

The client gathers all ICE candidates before sending the offer. The server gathers all ICE candidates before returning the answer. No trickle ICE.

### DataChannel — Event Messages

The client creates a DataChannel labelled `"events"` before generating the offer. Once the WebRTC connection is established, the server sends JSON text messages on it:

| Type | Payload | Description |
|------|---------|-------------|
| `transcript` | `{ text: string, final: boolean }` | User speech transcription (partial + final) |
| `response` | `{ text: string }` | LLM response chunks (streamed) |
| `error` | `{ message: string }` | Server-side errors |

### RFC 9725 Compliance

This implementation follows and aligns with [RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html):

- ✅ `POST` with `application/sdp` offer → `201 Created` with SDP answer (§4.2)
- ✅ `Location` header pointing to WHIP session URL (§4.2)
- ✅ `ETag` header identifying the ICE session (§4.3.1)
- ✅ `DELETE` on session URL for teardown (§4.2)
- ✅ `OPTIONS` with `Accept-Post: application/sdp` for CORS (§4.2)
- ✅ Full ICE gathering (no trickle ICE) on both client and server (§4.3.2)

**Bidirectional extensions:** WHIP was designed for unidirectional ingestion. This project extends it for bidirectional voice by using `sendrecv` (allowed per §4.2: client "MAY use sendrecv") and adding a DataChannel for server→client event delivery.

## Pipeline Architecture

The server uses a **channel-based streaming pipeline** — no giant buffers, no dumb forwarding. Each session runs four goroutines connected by bounded channels:

```
Browser mic → WebRTC → RTP read → Opus decode → PCM frames
    → VAD (barge-in detection) + STT feed
    → Agent orchestrator (LLM → TTS)
    → Opus encode → RTP write → WebRTC → Browser speaker
```

| Goroutine | Channel In | Channel Out | Responsibility |
|-----------|-----------|-------------|----------------|
| **Reader** | Remote track | `inPCMCh` | RTP read → Opus decode → 20ms PCM frames |
| **Inbound** | `inPCMCh` | `transcriptCh` | Feed STT + VAD barge-in detection |
| **Agent** | `transcriptCh` | `outPCMCh` | LLM streaming → TTS synthesis → PCM frames |
| **Sender** | `outPCMCh` | Local track | Opus encode → RTP write with wall-clock pacing |

**Barge-in:** When the user speaks while the agent is talking, the VAD fires `interruptCh`. The agent cancels the current LLM/TTS response, drains the outbound queue, and waits for the next transcript.

All channels are bounded to prevent latency creep.

## Docker

| File | Purpose |
|------|---------|
| `server/Dockerfile` | Multi-stage Go build (pure Go, no CGO) |
| `examples/typescript/Dockerfile` | Multi-stage Next.js build |
| `docker-compose.yml` | Orchestrates server + client |

The server reads config from `server/config.toml` (mounted as a volume). Create it from `server/config.toml.example` before running `docker compose up`.

## Project Structure

```
streamcoreai/
├── docker-compose.yml
├── package.json              # npm workspaces root
│
├── server/                   # Go backend
│   ├── config.toml.example
│   ├── Dockerfile
│   ├── main.go
│   └── internal/
│       ├── audio/            # Opus encode/decode, PCM utilities
│       ├── config/           # TOML configuration loader
│       ├── llm/              # LLM provider interface (OpenAI)
│       ├── peer/             # Pion WebRTC peer + track setup
│       ├── pipeline/         # Channel-based streaming pipeline
│       ├── session/          # Session manager, per-session peers
│       ├── signaling/        # WHIP HTTP signaling (RFC 9725)
│       ├── stt/              # STT provider interface (Deepgram, OpenAI Whisper)
│       ├── tts/              # TTS provider interface (Cartesia, Deepgram, ElevenLabs)
│       └── vad/              # Energy-based voice activity detector
│
├── typescript-sdk/           # @streamcore/js-sdk (npm)
│   └── src/                  # StreamCoreAIClient, WHIP, types
│
├── golang-sdk/               # github.com/streamcoreai/voice-agent-sdk-go
│   ├── client.go             # Client, Connect/Disconnect
│   ├── whip.go               # WHIP signaling
│   └── types.go              # Config, EventHandler, types
│
├── rust-sdk/                 # streamcoreai-voice-agent-sdk (Rust/Crates.io)
│   ├── src/
│   │   ├── client.rs         # Client, Connect/Disconnect
│   │   ├── whip.rs           # WHIP signaling
│   │   ├── types.rs          # Config, EventHandler, types
│   │   └── lib.rs
│   └── Cargo.toml
│
├── examples/
│   ├── typescript/           # Next.js client (uses typescript-sdk)
│   │   └── src/
│   │       ├── app/           # Next.js app router
│   │       ├── components/    # StreamCoreAI, AudioVisualizer
│   │       └── hooks/         # useStreamCoreAI (wraps SDK)
│   ├── golang/               # Go CLI example (uses golang-sdk)
│   │   └── main.go
│   └── rust/                 # Rust CLI example (uses rust-sdk)
│       └── src/main.rs
│
└── README.md
```

## Scaling

The session manager is in-memory (single process). For horizontal scaling, add a Redis-backed session manager with pub/sub for cross-instance signaling.

## Contributing

Contributions welcome. See [TODO](#todo) for planned features.

## License

Apache 2.0 — see [LICENSE](LICENSE).
