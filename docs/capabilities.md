**English** | [简体中文](./capabilities.zh-CN.md)

# Capabilities

What the media runtime does today. Anything not here is in [Roadmap](./roadmap.md), not in the product yet.

Use StreamCore to build:

- voice agents
- realtime copilots
- live translation
- AI-hosted audio experiences
- embedded voice devices
- phone and communication applications
- custom realtime AI products

## The problem StreamCore solves

Building an agent demo is easy. Building reliable realtime media infrastructure around it is not.

Once a prototype has to become a product, the hard parts are not prompts:

| Problem | What StreamCore does today |
|---------|----------------------------|
| WebRTC connectivity | Pion-based peer, full ICE gathering, WHIP signaling over a single HTTP POST |
| NAT traversal | Built-in STUN/TURN server — no external coturn container |
| Audio transport | Opus over RTP in both directions, decode/encode handled for you |
| Turn-taking | Adaptive VAD that tracks each call's noise floor, plus a debounce that merges a caller's mid-sentence pauses into one turn |
| Interruption | Barge-in on a faster VAD profile: agent audio ducks first, backchannels ("mm-hm", "yeah okay") are filtered out, and a confirmed interrupt cancels in-flight LLM and TTS |
| Streaming provider integration | Streaming STT, streaming LLM, and chunk-streaming TTS so audio starts before synthesis finishes — wired end to end |
| Session state | Server-generated session IDs, multi-peer sessions, lifecycle and teardown |
| Realtime events | DataChannel events for transcript, response, agent state, and latency timings |
| Client integration | SDKs for TypeScript, React Native, Python, Go, and Rust |
| Telephony | SIP bridge component that transcodes PCMU ↔ Opus and connects over WHIP |
| Auth | Optional JWT auth on `/whip` with a short-lived token endpoint |
| Latency visibility | DataChannel timing events, plus a per-turn latency breakdown (endpointing, merge, embedding, vector search, LLM, TTS) in the logs |

## How it fits

```text
┌───────────────────────────────────────────────┐
│              Applications                     │
│ Voice agents · Copilots · Translation · Rooms │
└───────────────────────┬───────────────────────┘
                        │ SDKs and realtime events
┌───────────────────────▼───────────────────────┐
│           StreamCore Media Runtime            │
│                                               │
│ WebRTC · RTP · Opus · Sessions · Interruption │
│ VAD · Streaming audio · Network traversal     │
└───────────────┬───────────────────┬───────────┘
                │                   │
       ┌────────▼────────┐  ┌───────▼──────────┐
       │ AI and speech   │  │ Application and  │
       │ services        │  │ agent backends   │
       │ STT · TTS · LLM │  │ Tools · APIs     │
       └─────────────────┘  └──────────────────┘
```

StreamCore can run a complete speech-to-agent-to-speech pipeline, but that is only one way to use it.

## Realtime media capabilities

**Transport and connectivity**

- Bidirectional Opus audio over WebRTC (`sendrecv`)
- WHIP signaling ([RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html)) — one HTTP `POST` for SDP exchange, no persistent signaling socket
- Full ICE gathering on both sides, no trickle ICE
- ICE restart over `PATCH /whip/{sessionId}` — a network handover or NAT rebind is recovered on the same connection, so the conversation, the pipeline, and the LLM client all survive it
- Idle sessions reaped after a configurable grace period, so a client that vanishes mid-call is collected without cutting short one that is reconnecting
- Built-in STUN/TURN server using Pion (UDP **and TCP** 3478, relay range 50001–60000) — TCP so callers behind UDP-blocking firewalls still connect
- Optional JWT auth on `/whip`, with `POST /token` issuing 1-hour tokens
- `/health` endpoint and graceful shutdown with a forced-exit safety net

**Media path**

- Opus decode → PCM → pipeline → PCM → Opus encode → RTP
- Energy-based VAD with configurable onset/offset frame counts, adapting to each call's noise floor so a quiet caller on a clean line and a caller beside a road both register
- Barge-in on a faster VAD profile: agent audio ducks while the caller talks over it and recovers if the interruption turns out to be a backchannel
- Turn debounce that merges consecutive final transcripts, so "I want to… um… book a table" is answered once, not twice
- Sentence-boundary chunking so TTS starts before the LLM finishes, and chunk-level streaming so audio plays before a sentence is fully synthesized
- Optional per-utterance delivery tags — the model may prefix a sentence with `[warm]`, `[empathetic]`, `[calm]`, or `[excited]`, which map to provider voice controls and are never spoken aloud
- Thinking sound — an optional tone played through the RTP stream while a slow tool runs (500 ms grace period)

**Sessions and events**

- Server-generated session IDs, in-memory session manager
- Multiple peers per session, each with an inbound or outbound direction
- DataChannel `events` channel for `transcript`, `response`, `state`, and `timing`
- Per-turn latency breakdown logged when `pipeline.debug = true`, separating endpointing, turn merge, embedding, vector search, LLM, and TTS
- Inbound DataChannel messages routed into the pipeline (used today for camera image chunks)

**Clients**

- TypeScript (`@streamcore/js-sdk`), Python (`streamcore`), Go (`github.com/streamcoreai/go-sdk`), Rust
- React Native / Expo (`@streamcore/react-native-sdk`) — built, not yet published to npm

## Supported endpoints

| Endpoint type | Status | How |
|---------------|--------|-----|
| Browser | Available | TypeScript SDK over WHIP |
| Mobile | Available | React Native / Expo SDK (`react-native-webrtc` peer dependency) |
| Backend service / worker | Available | Go, Python, or Rust SDK |
| CLI and TUI | Available | Go and Rust examples |
| Telephony (SIP) | Available | [`sip-server`](https://github.com/streamcoreai/sip-server) bridges PCMU/RTP ↔ Opus/WHIP, inbound and outbound |
| Embedded device | Experimental | ESP32-S3 firmware in [`esp32`](https://github.com/streamcoreai/esp32) speaking WHIP directly |

## AI integrations

| AI integration | Providers |
|----------------|-----------|
| Streaming STT | Deepgram, AssemblyAI, OpenAI, VibeVoice (local) |
| LLM | OpenAI, Ollama (local or self-hosted) |
| Streaming TTS | Cartesia, Deepgram, ElevenLabs, MiniMax, Speechify, VibeVoice (local) |
| Speech-to-speech | xAI Grok Voice (replaces STT + LLM + TTS in one model) |
| Retrieval | pgvector, Supabase |
| Custom tools | Python / TypeScript / JavaScript plugins, native Go tools |

Credentials and per-provider caveats: [Providers](./providers.md).
