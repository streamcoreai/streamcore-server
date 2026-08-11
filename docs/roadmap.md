**English** | [简体中文](./roadmap.zh-CN.md)

# Roadmap / TODO

Everything on this page is **not built yet**. It lives here so the [capability tables](./capabilities.md) stay honest — if a feature is listed on this page, do not plan around it today.

Want one of these? Say so in [Discord](https://discord.gg/xKGFaGWawT) or open an issue — demand reorders this list. Contributions welcome; see [CONTRIBUTING.md](../CONTRIBUTING.md).

## Production hardening

- [ ] **Horizontal scaling.** Session state is in-memory and single-process. Multi-instance deployments need sticky routing or external session coordination today.
- [ ] **Client-driven reconnection.** The server recovers a dropped connection on the same session via ICE restart (`PATCH /whip/{sessionId}`, see [Protocol](./protocol.md#ice-restart)), but no SDK drives it yet: none calls `restartIce()` or sends the PATCH, so a client whose network changes still redials and starts a new session.
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
