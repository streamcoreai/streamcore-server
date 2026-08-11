**English** | [简体中文](./roadmap.zh-CN.md)

# Roadmap / TODO

Everything **unticked** on this page is **not built yet**. It lives here so the [capability tables](./capabilities.md) stay honest — if a feature is unticked on this page, do not plan around it today.

Ticked items have shipped. They stay listed for a release or two so the page shows what moved, and each links to the docs for the feature as built.

Want one of these? Say so in [Discord](https://discord.gg/xKGFaGWawT) or open an issue — demand reorders this list. Contributions welcome; see [CONTRIBUTING.md](../CONTRIBUTING.md).

## Production hardening

- [x] **Session reconnection (server).** A dropped connection is recovered on the same session via ICE restart (`PATCH /whip/{sessionId}`) instead of costing the call. The `PeerConnection`, the DTLS association, the running pipeline, and the LLM client all survive it, so the conversation history and rolling summary do too and the greeting does not replay. Sessions left behind by a client that vanished are reaped after `server.session_grace_ms`. See [Protocol → ICE restart](./protocol.md#ice-restart).
- [x] **Client-driven reconnection.** The TypeScript, React Native, Go, and Rust SDKs detect the drop and recover it without the host app doing anything: three backed-off ICE restarts inside the ~25s window, then two resume redials if the connection failed anyway. The one thing an app should handle is the `recovered-without-history` outcome — a working call whose agent has forgotten the conversation. See [Protocol → The recovery ladder](./protocol.md#the-recovery-ladder), and each SDK's README for the knobs.
- [x] **Session resume.** ICE restart cannot help once a connection is `failed` — the peer is closed and there is nothing left to restart. A redial carrying a single-use token reattaches to the running conversation instead, preserving the LLM history, the transcript log and the rolling summary, and skipping the greeting. Every SDK runs restart-then-resume as one ladder; Python has only the resume half, since aiortc offers no ICE restart primitive (`createOffer()` takes no options, aioice fixes its credentials at construction) and no `disconnected` state to trigger on. See [Protocol → The recovery ladder](./protocol.md#the-recovery-ladder).
- [ ] **Panic recovery.** A panic in one session's pipeline goroutine takes down the process and every live call with it. Per-session goroutines should recover, log the stack, and tear down only their own session.
- [ ] **Session cap.** Rate limiting is per client IP; there is no global `max_sessions`. Each session burns CPU and provider spend, so distributed clients can still pile on. A cap returning 503 with `Retry-After` bounds the blast radius.
- [ ] **Env-var secrets.** API keys can only live in `config.toml` — nothing reads the environment. Container and cloud deployments want keys injected as environment variables, so they stay out of images and files.
- [ ] **Metrics and observability.** `/health` and DataChannel timing events exist; there is no Prometheus/OpenTelemetry export.
- [ ] **Structured logging.** Everything is `log.Printf` text. Production wants JSON logs carrying `session_id` on every line — `log/slog` with a session-scoped logger, migrated incrementally.
- [ ] **Versioned releases.** CI builds and tests, but there is no release workflow, no version embedded in the binary, and no published Docker image or tagged binaries — every user builds from source.

## Bring-your-own-agent

- [x] **HTTP agent endpoint.** Shipped as `llm.provider = "agent"` — a webhook-style backend so [bring-your-own-agent](./bring-your-own-agent.md) needs no Go code. An OpenAI-compatible `base_url` mode remains open.
- [ ] **Persistent memory.** The built-in runtime forgets callers between sessions — the rolling summary lives and dies with a call. [BYO agents](./bring-your-own-agent.md) can already persist their own.

## Ecosystem

- [ ] **Broader examples** proving the positioning: realtime translator, AI-hosted voice room, browser copilot, embedded device, SIP application, and a raw audio-processing app with no LLM at all.
- [ ] **Embedded client hardening.** The ESP32-S3 firmware in [`esp32`](https://github.com/streamcoreai/esp32) connects over WHIP but is not production-ready.
- [ ] **`streamcore-cli` release.** Document ingestion for RAG is written against a binary that is not public yet — see [Agent runtime → Ingesting documents](./agent-runtime.md#ingesting-documents).
- [ ] **React Native SDK on npm.** `@streamcore/react-native-sdk` is built and usable from source, but unpublished.

---

Already shipped and supported: [Capabilities](./capabilities.md).
