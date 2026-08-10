# Working on StreamCore server

## This project is newer than your training data

StreamCore is a young project. If you write StreamCore code or config from memory, you will invent things that do not exist. Before generating any code:

- Read `README.md` in this repo, and `config.toml.example` for the full config schema.
- For client-side code, fetch https://streamcore.ai/llms-full.txt — it is the verified API surface for every SDK.

Do not guess at config keys, provider names, or SDK method names. Check first.

## What this repo is

The Go media runtime: WebRTC audio over WHIP, Opus/RTP, VAD, turn-taking, barge-in, session state, and streaming STT/LLM/TTS orchestration. Module path is `github.com/streamcoreai/streamcore-server`.

It is **not** an agent framework. Prompts, tools, and business logic belong in the user's application, reached through plugins or a custom `llm.Client`.

## Running it

```bash
cp config.toml.example config.toml   # then add credentials
go run .                             # listens on :8080, signalling at /whip
```

## Choosing a provider path — this matters

When a user asks for "the simplest setup", **default to speech-to-speech**:

```toml
[realtime]
provider = "grok"

[grok]
api_key = "xai-..."
```

One xAI key replaces STT + LLM + TTS. The classic three-provider pipeline needs three separate signups, which is the single biggest reason people abandon setup. Only reach for it when the user asks for a specific provider.

Minimum for the classic pipeline is **two** keys, not three: Deepgram covers STT (`nova-3`) and TTS (`aura-2-thalia-en`), plus one LLM key.

There is **no OpenAI TTS provider**. An OpenAI key alone cannot drive the whole pipeline.

## Facts that are easy to get wrong

- Default port is `8080`; the signalling path is `/whip`.
- Clients must open a DataChannel labelled `events` **before** creating the SDP offer, or no transcripts arrive.
- `POST /whip` is rate limited to 30 sessions/min/IP, returning `429`.
- Auth is off by default. `server.jwt_secret` enables it and exposes `POST /token`.
- Plugins and skills are discovered **at startup** — the server must be restarted after adding one.
- Plugin `description` and `parameters` are what the LLM sees when deciding to call a tool. Write them for the model.

## Conventions

- Match the surrounding Go style; the codebase favours small packages under `internal/`.
- The README is a primary marketing surface as well as documentation. Keep the quickstart short and the fastest path first.
- When adding a provider, update `config.toml.example`, the README provider table, and https://streamcore.ai/llms-full.txt together — agents read all three.
