# Architecture and implementation

```text
┌─────────────────────┐                    ┌─────────────────────────────────────┐
│  Client / SDK / SIP │                    │      StreamCore Runtime (Go)        │
│                     │                    │                                     │
│  Mic → WebRTC ──────┼──── Opus RTP ──────┼──→ Opus decode → VAD → STT          │
│  Speaker ← WebRTC ←─┼──── Opus RTP ←─────┼──← Opus encode ← TTS                │
│                     │                    │               │                     │
│  HTTP POST ─────────┼── WHIP (SDP) ──────┼──→ Peer + session created           │
│  DataChannel ◄──────┼──── events   ←─────┼──← transcript · response · state    │
│                     │                    │               │                     │
│                     │                    │               ├── your LLM client   │
│                     │                    │               ├── RAG context       │
│                     │                    │               ├── Skills prompt     │
│                     │                    │               ├── Plugin runtime    │
│                     │                    │               │   ├── Python        │
│                     │                    │               │   ├── TypeScript    │
│                     │                    │               │   └── JavaScript    │
│                     │                    │               └── Native Go tools   │
└─────────────────────┘                    └─────────────────────────────────────┘
```

**Media flow.** Microphone audio arrives over WebRTC, is decoded to PCM in 20 ms frames, run through VAD, and streamed to STT. Final transcripts go to the model layer; streamed output is split on sentence boundaries and handed to TTS as it arrives, so synthesis starts before generation finishes. Synthesized PCM is encoded back to Opus and written to the RTP stream. Transcript, response, and state text travel over the DataChannel in parallel.

**Why Go.** The latency-sensitive path is implemented in Go with [Pion](https://github.com/pion/webrtc): goroutines per stage, bounded channels between them, and no GC-heavy buffering in the hot loop. RTP read, Opus decode, VAD, STT streaming, orchestration, TTS, Opus encode, and RTP write are each their own stage. This is an implementation choice in service of predictable turn latency — the surface you build against is the SDKs and the event protocol, in whatever language you prefer.

## Package layout

| Package | Responsibility |
|---------|----------------|
| [`internal/signaling`](../internal/signaling/) | WHIP handler, SDP exchange, session URLs |
| [`internal/peer`](../internal/peer/) | Pion peer connection, tracks, DataChannel |
| [`internal/session`](../internal/session/) | Session manager, multi-peer lifecycle |
| [`internal/pipeline`](../internal/pipeline/) | Inbound/outbound audio, agent loop, barge-in, thinking sound |
| [`internal/audio`](../internal/audio/) | Opus codec, RTP framing |
| [`internal/vad`](../internal/vad/) | Energy-based voice activity detection |
| [`internal/stt`](../internal/stt/), [`internal/tts`](../internal/tts/), [`internal/llm`](../internal/llm/) | Provider adapters |
| [`internal/plugin`](../internal/plugin/) | Plugin runtime, native tools, skills |
| [`internal/rag`](../internal/rag/) | Retrieval and embeddings |
| [`internal/turn`](../internal/turn/) | Built-in STUN/TURN server |

Wire-level details: [Protocol reference](./protocol.md).
