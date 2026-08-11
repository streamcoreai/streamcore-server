**English** | [简体中文](./roadmap.zh-CN.md)

# Roadmap / TODO

Everything **unticked** on this page is **not built yet**. It lives here so the [capability tables](./capabilities.md) stay honest — if a feature is unticked on this page, do not plan around it today.

Ticked items have shipped. They stay listed for a release or two so the page shows what moved, and each links to the docs for the feature as built.

Want one of these? Say so in [Discord](https://discord.gg/xKGFaGWawT) or open an issue — demand reorders this list. Contributions welcome; see [CONTRIBUTING.md](../CONTRIBUTING.md).

## Production hardening

- [ ] **Horizontal scaling.** Session state is in-memory and single-process. Multi-instance deployments need sticky routing or external session coordination today.
- [x] **Session reconnection (server).** A dropped connection is recovered on the same session via ICE restart (`PATCH /whip/{sessionId}`) instead of costing the call. The `PeerConnection`, the DTLS association, the running pipeline, and the LLM client all survive it, so the conversation history and rolling summary do too and the greeting does not replay. Sessions left behind by a client that vanished are reaped after `server.session_grace_ms`. See [Protocol → ICE restart](./protocol.md#ice-restart).
- [x] **Client-driven reconnection.** The TypeScript, React Native, Go, and Rust SDKs detect the drop, re-gather ICE, and PATCH the restart automatically, backing off across three attempts inside the ~25s window before the connection is declared failed. Nothing is required of the host app beyond reacting to the new `reconnecting` status if it wants to. See [Protocol → ICE restart](./protocol.md#ice-restart) for the exchange, and each SDK's README for the knobs.
- [ ] **Reconnection in the Python SDK.** Python is the one client that cannot drive the above: aiortc has no ICE restart primitive (`createOffer()` takes no options, aioice fixes its credentials at construction) and no `disconnected` connection state to trigger on — it goes straight from `connected` to `failed`. The wire-format helpers ship in `streamcore.icerestart` for callers on another WebRTC stack, but a Python client whose network changes still redials into a new session.
- [ ] **Metrics and observability.** `/health` and DataChannel timing events exist; there is no Prometheus/OpenTelemetry export.

## Bring-your-own-agent

- [ ] **HTTP agent endpoint.** A configurable OpenAI-compatible or webhook-style agent backend, so [bring-your-own-agent](./bring-your-own-agent.md) needs no Go code. Today the equivalent is implementing one small Go interface.
- [ ] **Persistent memory across sessions.** The rolling summary lives and dies with a call.

## Ecosystem

- [ ] **Broader examples** proving the positioning: realtime translator, AI-hosted voice room, browser copilot, embedded device, SIP application, and a raw audio-processing app with no LLM at all.
- [ ] **Embedded client hardening.** The ESP32-S3 firmware in [`esp32`](https://github.com/streamcoreai/esp32) connects over WHIP but is not production-ready.
- [ ] **`streamcore-cli` release.** Document ingestion for RAG is written against a binary that is not public yet — see [Agent runtime → Ingesting documents](./agent-runtime.md#ingesting-documents).
- [ ] **React Native SDK on npm.** `@streamcore/react-native-sdk` is built and usable from source, but unpublished.

---

Already shipped and supported: [Capabilities](./capabilities.md).
