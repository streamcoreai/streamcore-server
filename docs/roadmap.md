**English** | [简体中文](./roadmap.zh-CN.md)

# Roadmap / TODO

Everything **unticked** on this page is **not built yet**. It lives here so the [capability tables](./capabilities.md) stay honest — if a feature is unticked on this page, do not plan around it today.

Ticked items have shipped. They stay listed for a release or two so the page shows what moved, and each links to the docs for the feature as built.

Want one of these? Say so in [Discord](https://discord.gg/xKGFaGWawT) or open an issue — demand reorders this list. Contributions welcome; see [CONTRIBUTING.md](../CONTRIBUTING.md).

## Production hardening

- [x] **Session reconnection (server).** A dropped connection is recovered on the same session via ICE restart (`PATCH /whip/{sessionId}`) instead of costing the call. The `PeerConnection`, the DTLS association, the running pipeline, and the LLM client all survive it, so the conversation history and rolling summary do too and the greeting does not replay. Sessions left behind by a client that vanished are reaped after `server.session_grace_ms`. See [Protocol → ICE restart](./protocol.md#ice-restart).
- [x] **Client-driven reconnection.** The TypeScript, React Native, Go, and Rust SDKs detect the drop and recover it without the host app doing anything: three backed-off ICE restarts inside the ~25s window, then two resume redials if the connection failed anyway. The one thing an app should handle is the `recovered-without-history` outcome — a working call whose agent has forgotten the conversation. See [Protocol → The recovery ladder](./protocol.md#the-recovery-ladder), and each SDK's README for the knobs.
- [x] **Session resume.** ICE restart cannot help once a connection is `failed` — the peer is closed and there is nothing left to restart. A redial carrying a single-use token reattaches to the running conversation instead, preserving the LLM history, the transcript log and the rolling summary, and skipping the greeting. Every SDK runs restart-then-resume as one ladder; Python has only the resume half, since aiortc offers no ICE restart primitive (`createOffer()` takes no options, aioice fixes its credentials at construction) and no `disconnected` state to trigger on. See [Protocol → The recovery ladder](./protocol.md#the-recovery-ladder).
- [x] **Panic recovery.** A panic in one session's pipeline goroutine no longer takes down the process and every live call with it. Every pipeline goroutine recovers, logs the stack, and cancels only its own pipeline; the peer is then closed and the session reaped like any other ended call. Best-effort goroutines (greeting, rolling summary, RAG prefetch, thinking sound) recover without ending the call at all.
- [x] **Session cap.** `server.max_sessions` caps live sessions globally, on top of the per-IP rate limit. Past the cap, `POST /whip` returns 503 with `Retry-After`; resumes are exempt because they reattach to a session that is already counted. Zero (the default) is unlimited. See [Configuration](./configuration.md).
- [x] **Env-var secrets.** Every secret can be injected as an environment variable (`OPENAI_API_KEY`, `DEEPGRAM_API_KEY`, `STREAMCORE_JWT_SECRET`, …) instead of written into `config.toml`, so keys stay out of images and files. A set variable overrides the file value and the override is logged by name, never by value. See [Configuration → Secrets from environment variables](./configuration.md#secrets-from-environment-variables).
- [ ] **Metrics and observability.** `/health` and DataChannel timing events exist; there is no Prometheus/OpenTelemetry export.
- [ ] **Structured logging.** Everything is `log.Printf` text. Production wants JSON logs carrying `session_id` on every line — `log/slog` with a session-scoped logger, migrated incrementally.
- [ ] **Versioned releases.** A Docker image is built and pushed to GHCR when a GitHub release is published. Still missing: a version embedded in the binary (`-ldflags`), tagged standalone binaries (goreleaser), and a changelog — a user cannot yet ask a running server what version it is.
- [ ] **Horizontal scaling story.** Sessions and resume tokens live in process memory, so the server is single-node: behind a plain load balancer, ICE restart and resume land on the wrong instance and break. Either a documented sticky-routing deployment pattern or an external session/token store.
- [ ] **Supply-chain CI.** CI runs `go test -race`, but there is no `govulncheck`, linting, Dependabot, or image scanning. Table stakes for open-source infrastructure, and each is a one-file addition.
- [ ] **Load testing.** Nothing answers "how many concurrent calls per core" — the first question anyone deploying voice infrastructure asks, and the number a sane `max_sessions` default needs.
- [ ] **pprof.** A long-running media server wants `net/http/pprof` on an internal port, so the first memory-growth report can be debugged from a live process.

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
