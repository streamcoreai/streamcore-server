<div align="center">

<img src="./assets/logo.png" alt="StreamCore" width="320" />

# StreamCore

### Realtime media infrastructure for AI-powered applications

**Talk to your AI over WebRTC — with interruption, streaming speech, and NAT traversal handled.**<br/>
One Go binary. Bring your own agent.

[![CI](https://github.com/streamcoreai/streamcore-server/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/streamcoreai/streamcore-server/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/streamcoreai/streamcore-server?logo=go&logoColor=white)](./go.mod)
[![WHIP RFC 9725](https://img.shields.io/badge/WHIP-RFC%209725-6f42c1)](https://www.rfc-editor.org/rfc/rfc9725.html)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](./LICENSE)
[![Stars](https://img.shields.io/github/stars/streamcoreai/streamcore-server?logo=github&color=f5c518)](https://github.com/streamcoreai/streamcore-server/stargazers)
[![Discord](https://img.shields.io/badge/join%20us%20on-discord-5865F2?logo=discord&logoColor=white)](https://discord.gg/xKGFaGWawT)
[![Follow @jasonshen_](https://img.shields.io/badge/follow-%40jasonshen__-000000?logo=x&logoColor=white)](https://x.com/jasonshen_)

[**Quick start**](#quick-start) · [**Docs**](./docs/) · [**Demo**](#demo) · [**SDKs**](#sdks-and-examples) · [**Roadmap**](./docs/roadmap.md) · [**Discord**](https://discord.gg/xKGFaGWawT) · [简体中文](./README.zh-CN.md)

</div>

---

Anyone can demo a voice agent. Then a real caller talks over it, pauses mid-sentence, dials in from behind a firewall that blocks UDP, or waits three seconds for the first word — and the demo stops being a product.

StreamCore is the layer that handles all of that. It owns the latency-sensitive media path between your users and your AI: **WebRTC transport, adaptive turn-taking, barge-in, streaming STT/LLM/TTS, NAT traversal, session state, and realtime events** — across browsers, phones, backends, telephony, and embedded devices.

What it deliberately does *not* own is your agent. Keep your prompts, tools, models, and business logic exactly where they are — [four supported ways](#bring-your-own-agent), no fork required.

Built with it: voice agents, realtime copilots, live translation, AI-hosted audio rooms, embedded voice devices, and phone applications.

## Demo

<a href="https://www.loom.com/share/ee079aca75aa4fa1ba6a5e51302fbd56" target="_blank">
  <img src="https://cdn.loom.com/sessions/thumbnails/ee079aca75aa4fa1ba6a5e51302fbd56-e4ee3f1f1a14a51d.jpg" alt="Demo Video" />
</a>

## Quick start

**Two terminals, five minutes, and you are talking to it.** Needs Go 1.25+ (or Docker) and API keys for an STT, LLM, and TTS provider. No keys? Run it [fully local](./docs/quickstart.md#fully-local-no-api-keys) with Ollama + VibeVoice.

```bash
cp config.toml.example config.toml   # add your provider credentials
go run .
```

The server listens on `:8080`; clients connect to `http://localhost:8080/whip`.

Then talk to it from a browser:

```bash
git clone https://github.com/streamcoreai/examples.git
cd examples/typescript && npm install && npm run dev
```

Open [http://localhost:3000](http://localhost:3000) and start talking.

Docker, TURN ports, and production notes: [Quick start guide](./docs/quickstart.md).

## What you get

| | |
|---|---|
| **Transport** | WebRTC audio over WHIP ([RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html)) — one HTTP POST, no signaling socket. Opus/RTP both ways |
| **Connectivity** | Built-in Pion STUN/TURN on UDP *and* TCP 3478 — no external coturn. A network handover or NAT rebind is recovered by ICE restart on the same session, so the conversation survives it |
| **Turn-taking** | Adaptive VAD that tracks each call's noise floor, plus a debounce that merges mid-sentence pauses into one turn |
| **Interruption** | Barge-in that ducks agent audio, filters backchannels ("mm-hm"), and cancels in-flight LLM and TTS on a confirmed interrupt |
| **Streaming** | Streaming STT → streaming LLM → chunk-streaming TTS, so audio starts before synthesis finishes |
| **Sessions & events** | Server-generated session IDs, multi-peer sessions, DataChannel events for transcript, response, state, and per-turn latency |
| **Reach** | Browser, mobile, backend, CLI, [SIP telephony](https://github.com/streamcoreai/sip-server), and [ESP32](https://github.com/streamcoreai/esp32) endpoints |

Full capability list: [Capabilities](./docs/capabilities.md).

## Not built yet

Listed so the table above stays honest — unticked items are real gaps today, not soon-shipping promises. Ticked ones shipped recently and stay listed for a release or two so you can see what moved:

- [ ] **Horizontal scaling** — session state is in-memory and single-process
- [x] **Session reconnection (server)** — a dropped connection recovers on the same session via ICE restart, so the conversation and the running pipeline survive it
- [x] **Client-driven reconnection** — the TypeScript, React Native, Go and Rust SDKs drive that restart automatically after a network change
- [x] **Session resume** — a drop past the point ICE restart can help is recovered by redialling with a single-use token, reattaching to the running conversation; this is how the Python SDK reconnects, since aiortc has no ICE restart primitive
- [ ] **Metrics export** — `/health` and timing events exist, no Prometheus/OpenTelemetry
- [ ] **HTTP agent endpoint** — bring-your-own-agent needs a small Go file today, not config
- [ ] **Persistent memory** across sessions

Full TODO list, including ecosystem items: [Roadmap / TODO](./docs/roadmap.md). Want one of these? Say so in [Discord](https://discord.gg/xKGFaGWawT) — demand reorders the list.

## Bring your own agent

StreamCore starts one layer below prompt-and-tool frameworks: the media path. Your intelligence stays yours, four ways —

1. **Tool call** — plugins (Python/TS/JS) or native Go tools call into your existing backend
2. **Your endpoint** — point `llm.provider = "ollama"` at any Ollama-compatible URL you run
3. **Your code** — implement one small Go interface; the whole media path works unchanged
4. **Built in** — or use StreamCore's optional agent runtime with tools, skills, RAG, and history

Details and code: [Bring your own agent](./docs/bring-your-own-agent.md) · [Agent runtime](./docs/agent-runtime.md).

Providers: Deepgram, AssemblyAI, OpenAI, Cartesia, ElevenLabs, MiniMax, Speechify, Ollama, VibeVoice (local), xAI Grok Voice (speech-to-speech), pgvector/Supabase for retrieval. See [Providers](./docs/providers.md).

## Documentation

| Page | What's in it |
|------|--------------|
| [Quick start](./docs/quickstart.md) | Docker, TURN ports, connecting a client, wiring your backend, fully-local setup |
| [Capabilities](./docs/capabilities.md) | What the runtime does today, endpoints, AI integrations |
| [Bring your own agent](./docs/bring-your-own-agent.md) | Four ways to own the intelligence, including the `llm.Client` interface |
| [Agent runtime](./docs/agent-runtime.md) | Plugins, skills, RAG, document ingestion |
| [Providers](./docs/providers.md) | Grok speech-to-speech, MiniMax, local VibeVoice, per-provider caveats |
| [Configuration](./docs/configuration.md) | Full annotated `config.toml` reference |
| [Protocol](./docs/protocol.md) | WHIP signaling, DataChannel events, auth |
| [Architecture](./docs/architecture.md) | Media flow, why Go, package layout |

## SDKs and examples

Connect from anywhere — every SDK speaks the same WHIP + DataChannel protocol:

[![npm](https://img.shields.io/npm/v/@streamcore/js-sdk?logo=npm&logoColor=white&label=%40streamcore%2Fjs-sdk)](https://github.com/streamcoreai/js-sdk)
[![PyPI](https://img.shields.io/pypi/v/streamcore?logo=pypi&logoColor=white&label=streamcore)](https://github.com/streamcoreai/python-sdk)
[![Go](https://pkg.go.dev/badge/github.com/streamcoreai/go-sdk.svg)](https://pkg.go.dev/github.com/streamcoreai/go-sdk)
[![crates.io](https://img.shields.io/crates/v/streamcore-rust-sdk?logo=rust&logoColor=white&label=streamcore-rust-sdk)](https://github.com/streamcoreai/rust-sdk)

React Native / Expo (`@streamcore/react-native-sdk`) is built but not yet published to npm.

Plugin SDKs: `@streamcore/plugin` and `streamcore-plugin` in [plugin-sdk](https://github.com/streamcoreai/plugin-sdk). Runnable browser, CLI, and TUI apps: [examples](https://github.com/streamcoreai/examples).

## Sponsors & Supporters

<!-- The public live demo is powered by generous API credits from our sponsors. -->

<div align="center">
<!-- Logos will go here once received -->
</div>

Thank you! Interested in sponsoring? Reach out for logo placement on GitHub + demo page.

## Contributing

Read [CONTRIBUTING.md](./CONTRIBUTING.md) first — it covers running the server locally, the four checks CI runs before you push, and the extra care the timing-sensitive media path needs. Good places to start: [`good first issue`](https://github.com/streamcoreai/streamcore-server/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22) and [`help wanted`](https://github.com/streamcoreai/streamcore-server/issues?q=is%3Aissue+is%3Aopen+label%3A%22help+wanted%22).

Client SDKs, the SIP bridge, examples, and the ESP32 firmware live in their own repos under [`streamcoreai`](https://github.com/streamcoreai) — send those changes there.

## Security

Found a vulnerability? Don't open a public issue — report it privately through the [Security tab](https://github.com/streamcoreai/streamcore-server/security/advisories/new). [SECURITY.md](./SECURITY.md) covers scope, response targets, and the settings that matter on a public address — JWT auth on `/whip` above all.

## Star history

<!--
  Live chart from star-history.com. GitHub restricted the stargazers API to repo
  admins/collaborators on 2026-06-30, so the chart renders only when a sealed
  (encrypted) GitHub token is supplied. If it ever shows "GitHub restricted access
  to star data", that token has expired or been revoked — regenerate it at
  https://star-history.com/#streamcoreai/streamcore-server&Date under "Show
  real-time chart on your README.md" and replace sealed_token in all three URLs.
-->
<a href="https://www.star-history.com/?type=date&repos=streamcoreai%2Fstreamcore-server">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=streamcoreai/streamcore-server&type=date&theme=dark&legend=top-left&sealed_token=Fe6rTffC730520Ua9jYN4AQoEmFMNIwEPzp19cmksSRM4GuvuYib6iu6TxRTv0k51n0-B9kO6FI-N9-pJH6WB8XGn4GH-gKnIz-ou7n3ctqiKQ3IO9LuBg" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=streamcoreai/streamcore-server&type=date&legend=top-left&sealed_token=Fe6rTffC730520Ua9jYN4AQoEmFMNIwEPzp19cmksSRM4GuvuYib6iu6TxRTv0k51n0-B9kO6FI-N9-pJH6WB8XGn4GH-gKnIz-ou7n3ctqiKQ3IO9LuBg" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=streamcoreai/streamcore-server&type=date&legend=top-left&sealed_token=Fe6rTffC730520Ua9jYN4AQoEmFMNIwEPzp19cmksSRM4GuvuYib6iu6TxRTv0k51n0-B9kO6FI-N9-pJH6WB8XGn4GH-gKnIz-ou7n3ctqiKQ3IO9LuBg" />
 </picture>
</a>

## License

Apache 2.0. See [LICENSE](./LICENSE).
