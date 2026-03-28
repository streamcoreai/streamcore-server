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

This repository is the Go server component in the StreamCoreAI project family. For the split-repo layout, see [Repository layout](https://github.com/streamcoreai/docs/blob/main/repository-structure.md).

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
- **Client SDKs** for [TypeScript](https://github.com/streamcoreai/typescript-sdk), [Go](https://github.com/streamcoreai/voice-agent-sdk-go), [Python](https://github.com/streamcoreai/python-sdk), and [Rust](https://github.com/streamcoreai/rust-sdk)
- **Plugin SDKs** for [TypeScript](https://github.com/streamcoreai/plugin-sdk/tree/main/typescript) and [Python](https://github.com/streamcoreai/plugin-sdk/tree/main/python)
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

Telephony note: SIP and phone connectivity live in [streamcoreai/sip-server](https://github.com/streamcoreai/sip-server), which bridges calls into the same voice pipeline.

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

### Option A: Docker

```bash
cp config.toml.example config.toml
# Edit config.toml with your API keys

docker build -t streamcoreai-server .
docker run --rm -p 8080:8080 -v "$(pwd)/config.toml:/config.toml:ro" streamcoreai-server
```

Then connect a client to `http://localhost:8080/whip`. You can use the browser client from [streamcoreai/examples-typescript](https://github.com/streamcoreai/examples-typescript) or any of the SDKs linked below.

### Option B: Local Development

Start the server from this repository:

```bash
cp config.toml.example config.toml
# Edit config.toml with your API keys

go run .
```

In another terminal, run a client from its own repository. For example, with the browser app:

```bash
git clone https://github.com/streamcoreai/examples-typescript.git
cd examples-typescript
npm install
npm run dev
```

Then open [http://localhost:3000](http://localhost:3000). By default it connects to `http://localhost:8080/whip`.

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

- [Plugin Development Guide](https://github.com/streamcoreai/docs/blob/main/plugins.md)
- [Skills Development Guide](https://github.com/streamcoreai/docs/blob/main/skills.md)

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

If you need zero-IPC extensions, you can also register native Go tools directly in the server via `pluginMgr.RegisterNative(...)`. See the Go section in the [Plugin Development Guide](https://github.com/streamcoreai/docs/blob/main/plugins.md).

## SDKs And Examples

Client SDKs:

- [TypeScript SDK](https://github.com/streamcoreai/typescript-sdk)
- [Go SDK](https://github.com/streamcoreai/voice-agent-sdk-go)
- [Python SDK](https://github.com/streamcoreai/python-sdk)
- [Rust SDK](https://github.com/streamcoreai/rust-sdk)

Plugin SDKs:

- [TypeScript plugin SDK](https://github.com/streamcoreai/plugin-sdk/tree/main/typescript)
- [Python plugin SDK](https://github.com/streamcoreai/plugin-sdk/tree/main/python)

Examples:

- [TypeScript browser app](https://github.com/streamcoreai/examples-typescript)
- [Go CLI and TUI examples](https://github.com/streamcoreai/examples-golang)
- [Python examples](https://github.com/streamcoreai/examples-python)
- [Rust CLI and TUI examples](https://github.com/streamcoreai/examples-rust)

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
