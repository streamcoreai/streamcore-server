# Contributing to streamcore-server

This repo is the **Go media runtime**: WebRTC audio over WHIP, Opus/RTP, VAD,
turn-taking, barge-in, session state, and streaming STT/LLM/TTS orchestration.
Module path is `github.com/streamcoreai/streamcore-server`.

It is **not** an agent framework. Prompts, tools, and business logic belong in
the user's application, reached through the plugin system or a custom
`llm.Client`. That boundary is the main thing reviewers hold the line on — a PR
that moves application logic into the runtime will usually be redirected toward
a plugin.

Client SDKs, the SIP bridge, examples, and the ESP32 firmware live in their own
repos under [`streamcoreai`](https://github.com/streamcoreai). Send those
changes there.

Chat: [Discord](https://discord.gg/xKGFaGWawT) · Docs: <https://streamcore.ai>

## Running it

```bash
cp config.toml.example config.toml   # then add credentials
go run .                             # listens on :8080, signalling at /whip
```

`config.toml` is gitignored. Keep real keys out of issues, PR descriptions, and
pasted logs — and rotate anything you have leaked.

For the quickest working setup, use the speech-to-speech path: one xAI key
covers STT + LLM + TTS.

```toml
[realtime]
provider = "grok"

[grok]
api_key = "xai-..."
```

The classic three-provider pipeline needs a minimum of two keys, not three —
Deepgram covers both STT (`nova-3`) and TTS (`aura-2-thalia-en`), plus one LLM
key. `config.toml.example` is the authoritative config schema; don't guess at
keys.

## Before you push

Run exactly what CI runs — these four steps are the whole build job:

```bash
gofmt -l .          # must print nothing
go build ./...
go vet ./...
go test -race ./...
```

CI deliberately has no `paths:` filters, so every PR produces a status check and
the README badge always reflects the real state of `main`. The Go toolchain
version comes from `go.mod` via `go-version-file`, so bumping the `go` directive
is all it takes to move CI with it.

Formatting and vet failures are the single most common reason a PR sits.

## Where things live

| Package | Responsibility |
|---|---|
| `internal/signaling` | WHIP endpoint, SDP exchange, auth |
| `internal/peer` | Pion peer connection, ICE, DataChannel |
| `internal/session` | Session IDs, multi-peer state, lifecycle, teardown |
| `internal/audio` | RTP packetization, Opus encode/decode |
| `internal/vad` | Energy-based voice activity detection |
| `internal/pipeline` | Turn-taking, barge-in, response generation, transcript |
| `internal/stt`, `internal/tts`, `internal/llm` | Provider adapters |
| `internal/realtime` | Speech-to-speech providers |
| `internal/plugin`, `internal/tools` | Plugin runtime, native tools, skills |
| `internal/rag` | Retrieval and embeddings |
| `internal/turn` | Built-in STUN/TURN server |
| `internal/config` | TOML config schema and validation |
| `internal/procstat` | Per-turn latency accounting |

Most first contributions land in a provider adapter (`internal/stt`,
`internal/tts`, `internal/llm`) or in `internal/plugin` — both have several
existing implementations to copy the shape from.

## Changes that need care

The media path is timing-sensitive and hard to test from the outside. For
anything touching `internal/pipeline`, `internal/vad`, `internal/audio`, or
`internal/peer`:

- **Say how you verified it with real audio.** Unit tests are necessary and not
  sufficient. "Ran a call with Deepgram STT and interrupted mid-sentence;
  ducking fired at ~120ms and the in-flight TTS was cancelled" is the kind of
  detail that gets a media PR merged.
- **Don't regress barge-in.** Backchannels ("mm-hm", "yeah okay") must stay
  filtered; a confirmed interrupt must still cancel in-flight LLM *and* TTS.
- **Watch the latency breakdown.** The per-turn log line (endpointing, merge,
  embedding, vector search, LLM, TTS) is the fastest way to see what your change
  cost. Include a before/after if you touched anything in that path.
- **Add a test where the logic is testable.** `internal/pipeline`,
  `internal/config`, `internal/audio`, and `internal/llm` all have real test
  suites — extend them rather than adding a new pattern.

Changes to the WHIP wire protocol, the DataChannel event shape, or a config key
are breaking for anyone running StreamCore in production. Open an issue first
and include the migration story.

## Adding a provider adapter

1. Implement the interface in `internal/stt`, `internal/tts`, `internal/llm`, or
   `internal/realtime`, following the closest existing adapter.
2. Add its config block to `config.toml.example` with every key documented.
3. Wire it into `internal/config` validation so a typo fails at startup rather
   than mid-call.
4. Streaming is the point — TTS must chunk-stream so audio starts before
   synthesis finishes, and LLM output must stream sentence by sentence. A
   request/response adapter that buffers the whole utterance will be sent back.
5. Update the capability table in the README, and `README.zh-CN.md` alongside it.

## Pull requests

- One concern per PR; keep refactors separate from behaviour changes.
- Explain what breaks today, what your change does, and how you verified it.
- Match the surrounding code — naming, comment density, error handling.
- Keep the README honest: capability tables describe what exists today, and the
  Roadmap section lists what doesn't. Move items between them as you land work.
- If you use an AI assistant, have it read `AGENTS.md` and `config.toml.example`
  first. StreamCore is newer than any model's training data, and invented config
  keys and provider names are a recurring source of bad PRs.

## Good first issues

See [`good first issue`](https://github.com/streamcoreai/streamcore-server/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22)
and [`help wanted`](https://github.com/streamcoreai/streamcore-server/issues?q=is%3Aissue+is%3Aopen+label%3A%22help+wanted%22).
Comment on an issue before starting anything large.

## Security

Don't file vulnerabilities as public issues — see [SECURITY.md](SECURITY.md).

## License

Apache 2.0. See [LICENSE](LICENSE). By contributing you agree your contribution
is licensed under it.
