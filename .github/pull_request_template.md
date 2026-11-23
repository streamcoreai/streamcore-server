## Description

<!-- What breaks or is missing today, and what this change does. One concern per
PR — keep refactors separate from behaviour changes. -->

## Linked issue

<!-- e.g. "Fixes #123". Open an issue first for breaking changes — the WHIP wire
protocol, DataChannel event shapes, and config keys are production contracts. -->

Fixes #

## How this was tested

<!--
Unit tests are necessary and not sufficient for the media path
(internal/pipeline, vad, audio, peer) — say how you verified with real audio,
and which providers the test ran against.

Example: "go test -race ./... plus a live call with stt=deepgram, llm=openai,
tts=cartesia; interrupted mid-sentence and the in-flight TTS cancelled."
-->

- Providers tested against: <!-- e.g. stt=deepgram + llm=openai + tts=cartesia, realtime=grok, or "unit tests only" -->

## Checklist

- [ ] `gofmt -l .` prints nothing
- [ ] `go build ./...` passes
- [ ] `go vet ./...` passes
- [ ] `go test -race ./...` passes
- [ ] New or changed config keys are documented in `config.toml.example`, and the README provider table is updated if a provider was added
- [ ] No secrets, API keys, or `config.toml` in the diff
