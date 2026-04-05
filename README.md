# StreamCoreAI Server

**Open-source real-time voice agent server built on WebRTC, with multi-language client SDKs, plugin extensibility, and Markdown-based skills.**

StreamCoreAI keeps the latency-sensitive media and orchestration path in Go, while letting the rest of your stack stay in the languages your team already uses.

That means you can:

- run the core media pipeline in **Go**
- connect from **TypeScript, Python, Rust, or Go**
- extend the agent with **Python, TypeScript, or JavaScript plugins**
- register **native Go tools** inside the server when you want zero-IPC integrations
- shape behavior with **Markdown skills**

Most voice stacks force everything into one runtime. StreamCoreAI is built differently: keep the real-time path in Go, but let product, AI, and integration teams move faster in TypeScript and Python.

This repository is the Go server component in the StreamCoreAI project family.

## Why StreamCoreAI

StreamCoreAI is designed for teams building real-time AI voice products who want:

- **a fast Go core** for media, session handling, and orchestration
- **multi-language SDKs** so clients are not tied to one stack
- **plugin extensibility** without forcing every integration into Go
- **skills** that shape tone and behavior without burying everything in prompts or code
- **an open-source, self-hostable foundation** for browser, SDK, and telephony voice flows

It is a strong fit for:

- browser voice agents
- AI assistants
- internal copilots
- AI calling systems
- support agents
- custom vertical voice products

## Features

- **Real-time bidirectional voice** over WebRTC with Opus audio
- **WHIP signaling** ([RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html)) with a single HTTP POST for SDP exchange
- **Streaming STT** with Deepgram, plus OpenAI Whisper support for final-only transcription
- **Streaming LLM responses** with OpenAI and conversation history
- **Configurable TTS** with Cartesia, Deepgram, or ElevenLabs
- **Barge-in support** so users can interrupt the assistant mid-response
- **Plugin system** for Python, TypeScript, and JavaScript tools over JSON-RPC
- **Native Go tool interface** for zero-IPC extensions compiled into the server
- **Skills system** that injects Markdown instructions into the system prompt
- **Client SDKs** for TypeScript (`@streamcore/js-sdk`), Go (`github.com/streamcoreai/go-sdk`), Python (`streamcoreai-sdk`), and [Rust](https://github.com/streamcoreai/rust-sdk)
- **Plugin SDKs** for TypeScript (`@streamcore/plugin`) and Python (`streamcore-plugin`)
- **Health endpoint** at `/health`

## What Makes It Different

### Go where it matters

The hot path runs in Go with Pion WebRTC, goroutines, and bounded channels:

- RTP read and Opus decode
- STT streaming and VAD
- LLM orchestration and tool calls
- TTS synthesis
- Opus encode and RTP write

That keeps the real-time loop predictable and low-latency.

### SDKs in four languages

Clients can connect from:

- **TypeScript**
- **Python**
- **Rust**
- **Go**

That makes it practical to build browser apps, backend workers, CLI tools, test harnesses, and desktop integrations without reimplementing the protocol for each environment.

### Plugins and skills are separate layers

Plugins give the agent **capabilities**. Skills shape its **behavior**.

- Plugins call APIs, databases, calendars, CRMs, workflows, and internal tools
- Skills define tone, personality, guardrails, brand voice, and workflow guidance

This keeps business logic and behavioral instructions easier to manage than a single giant prompt.

## Architecture

```text
┌─────────────────────┐                    ┌─────────────────────────────────────┐
│    Client / SDK     │                    │          Go Server (Pion)           │
│                     │                    │                                     │
│  Mic → WebRTC ──────┼──── Opus RTP ──────┼──→ Opus Decode → STT               │
│  Speaker ← WebRTC ←─┼──── Opus RTP ←─────┼──← Opus Encode ← TTS               │
│                     │                    │               │                     │
│  HTTP POST ─────────┼── WHIP (SDP) ──────┼──→ Peer + session created          │
│  DataChannel ◄──────┼──── events   ←─────┼──← LLM streaming                   │
│                     │                    │               │                     │
│                     │                    │               ├── Skills prompt     │
│                     │                    │               ├── Plugin runtime    │
│                     │                    │               │   ├── Python        │
│                     │                    │               │   ├── TypeScript    │
│                     │                    │               │   └── JavaScript    │
│                     │                    │               └── Native Go tools   │
└─────────────────────┘                    └─────────────────────────────────────┘
```

Signaling flow: the client creates an SDP offer, gathers ICE candidates, and `POST`s it to `/whip`. The server creates a peer, gathers its ICE candidates, and returns the SDP answer with a server-generated session ID. No persistent signaling socket is required.

Pipeline flow: microphone audio enters over WebRTC, is decoded to PCM, sent through STT, passed to the LLM, optionally routed through tools, synthesized with TTS, encoded back to Opus, and streamed to the client. Transcript and response text are sent back over a WebRTC DataChannel.

Telephony note: SIP and phone connectivity are handled by a separate SIP bridge in the StreamCoreAI project family.

## Prerequisites

For Docker:

- Docker
- Docker Compose

For local development:

- Go 1.22+
- Node.js 20+ and npm
- Python 3.10+ if you want Python plugins or examples
- Rust 1.87+ if you want Rust SDKs or examples

Provider requirements:

| Role | Providers | Required credentials |
|------|-----------|----------------------|
| STT | `deepgram`, `openai` | Deepgram API key or OpenAI API key |
| LLM | `openai` | OpenAI API key |
| TTS | `cartesia`, `deepgram`, `elevenlabs` | Matching provider API key |

## Quick Start

The server has two modes:

- **Standard (WHIP)** — direct WebRTC between client and server, no SFU required
- **Mediasoup** — joins a mediasoup SFU room as a peer, consumes audio from other participants

### Standard Mode (WHIP)

This is the default mode. Clients connect directly to the server over WebRTC using WHIP signaling.

#### Option A: Docker

```bash
cp config.toml.example config.toml
# Edit config.toml with your API keys

docker build -t streamcoreai-server .
docker run --rm -p 8080:8080 -v "$(pwd)/config.toml:/config.toml:ro" streamcoreai-server
```

#### Option B: Local Development

```bash
cp config.toml.example config.toml
# Edit config.toml with your API keys

go run ./cmd/standard
```

Then connect a client to `http://localhost:8080/whip`. You can use the browser client from [streamcoreai/examples](https://github.com/streamcoreai/examples/tree/main/typescript) or any of the SDKs listed below.

For example, with the browser app:

```bash
git clone https://github.com/streamcoreai/examples.git
cd examples/typescript
npm install
npm run dev
```

Then open [http://localhost:3000](http://localhost:3000). By default it connects to `http://localhost:8080/whip`.

### Mediasoup Mode

In this mode the server joins a [mediasoup](https://mediasoup.org/) SFU room as a peer. It consumes audio from other participants in the room, runs it through the voice pipeline (STT → LLM → TTS), and produces audio back into the room. The signaling is based on the [mediasoup-demo](https://github.com/versatica/mediasoup-demo/).

#### 1. Run the mediasoup demo

Clone the [mediasoup-demo](https://github.com/versatica/mediasoup-demo/) and follow their instructions to run the server and client. Once running, open the demo client in your browser and join a room (e.g. `https://localhost:3443/?roomId=dev`). Make sure your microphone is enabled.

#### 2. Configure and start the StreamCoreAI mediasoup server

Add the mediasoup section to your `config.toml`:

```toml
[mediasoup]
signaling_url = "https://localhost:4443"
room_id = "dev"
origin_header = "https://localhost:4443"
```

Then start the mediasoup server:

```bash
go run ./cmd/mediasoup
```

#### 3. Dispatch an agent into the room

Send a `POST` to `/dispatch` to start an agent that joins the room:

```bash
curl -X POST http://localhost:8080/dispatch \
  -H "Content-Type: application/json" \
  -d '{"roomId": "dev"}'
```

Response:

```json
{ "agentId": "agent-1", "roomId": "dev" }
```

The agent joins the mediasoup room, consumes audio from existing peers, and produces its responses back into the room. You can speak in the browser client and the agent will respond.

To stop an agent:

```bash
curl -X DELETE http://localhost:8080/dispatch/agent-1
```

## Configuration

Use [`config.toml.example`](./config.toml.example) as your starting point:

```toml
[server]
port = "8080"

[plugins]
directory = "./plugins"

[pipeline]
barge_in = true
greeting = ""
greeting_outgoing = ""
debug = false

[stt]
provider = "deepgram"

[llm]
provider = "openai"

[tts]
provider = "cartesia"

[deepgram]
api_key = ""
model = "nova-3"

[openai]
api_key = ""
model = "gpt-4o-mini"
system_prompt = "You are a helpful AI voice assistant. Keep your responses concise and conversational."

[cartesia]
api_key = ""
voice_id = ""

[elevenlabs]
api_key = ""
voice_id = ""
model = ""
```

Notes:

- `plugins.directory` is required if you want plugins and skills loaded. If it is omitted, the server skips plugin discovery.
- `pipeline.barge_in` lets users interrupt the assistant while it is speaking.
- `pipeline.greeting` plays when a session starts. `pipeline.greeting_outgoing` is used for outbound SIP calls when present.
- `pipeline.debug = true` emits timing events over the DataChannel.
- `stt.provider = "openai"` uses Whisper-style final transcription instead of streaming partials.

## Plugins And Skills

Plugins give the LLM callable tools during a conversation. Skills inject Markdown instructions into the system prompt for every session.

- Plugin Development Guide
- Skills Development Guide

This repo already includes sample plugins and skills under [plugins/](./plugins/).

### Quick Plugin Example

Create a Python plugin that tells the time:

```bash
mkdir -p plugins/plugins/time-get
```

`plugins/plugins/time-get/plugin.yaml`

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

`plugins/plugins/time-get/main.py`

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

Restart the server, then ask the agent for the time in a specific timezone.

If you need zero-IPC extensions, you can also register native Go tools directly in the server via `pluginMgr.RegisterNative(...)`. See the Go section in the plugin development guide.

## SDKs And Examples

Client SDKs:

- TypeScript SDK: `@streamcore/js-sdk`
- Go SDK: `github.com/streamcoreai/go-sdk`
- Python SDK: `streamcoreai-sdk`
- [Rust SDK](https://github.com/streamcoreai/rust-sdk)

Plugin SDKs:

- TypeScript plugin SDK: `@streamcore/plugin`
- Python plugin SDK: `streamcore-plugin`

Examples:

- [TypeScript browser app](https://github.com/streamcoreai/examples/tree/main/typescript)
- [Go CLI example](https://github.com/streamcoreai/examples/tree/main/golang)
- [Go TUI example](https://github.com/streamcoreai/examples/tree/main/golang-tui)
- [Python examples](https://github.com/streamcoreai/examples/tree/main/python)
- [Rust CLI example](https://github.com/streamcoreai/examples/tree/main/rust)
- [Rust TUI example](https://github.com/streamcoreai/examples/tree/main/rust-tui)

## WHIP Protocol

Signaling follows [RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html).

### HTTP SDP Exchange

| Step | Method | Path | Body | Response |
|------|--------|------|------|----------|
| 1 | `POST` | `/whip` | SDP offer (`application/sdp`) | `201 Created` with SDP answer, `Location: /whip/{sessionId}`, and `ETag` |
| 2 | `DELETE` | `/whip/{sessionId}` | none | `200 OK` |
| - | `OPTIONS` | `/whip` or `/whip/{sessionId}` | none | `204 No Content` with `Accept-Post: application/sdp` |

The client gathers ICE candidates before sending the offer. The server gathers ICE candidates before returning the answer. No trickle ICE is used.

### DataChannel Events

The client must create a DataChannel labeled `events` before generating the offer. The server currently sends these JSON messages:

| Type | Payload | Description |
|------|---------|-------------|
| `transcript` | `{ "type": "transcript", "text": string, "final": boolean }` | User transcript updates |
| `response` | `{ "type": "response", "text": string }` | Streamed LLM response text |
| `timing` | `{ "type": "timing", "stage": string, "ms": number }` | Optional latency timings when `pipeline.debug = true` |

Current timing stages are:

- `llm_first_token`
- `tts_first_byte`

### RFC Notes

This implementation aligns with the core WHIP flow in RFC 9725:

- `POST` with `application/sdp`
- `201 Created` with SDP answer
- `Location` header for the session URL
- `ETag` header for the ICE session
- `DELETE` for teardown
- `OPTIONS` with `Accept-Post: application/sdp`
- full ICE gathering on both sides

The server uses `sendrecv` audio and a DataChannel to support bidirectional voice interaction.

## Scaling And Roadmap

Today, session management is in-memory and single-process. For horizontal scaling you will need sticky routing or external session coordination.

Near-term areas to build on:

- retrieval / RAG
- persistent memory across sessions
- more end-to-end SDK and plugin examples
- easier deployment and hosted workflows

## License

Apache 2.0. See [LICENSE](./LICENSE).
