# Moonshine STT Server

**English** | [简体中文](./README.zh-CN.md)

Live streaming speech-to-text server using [Moonshine](https://moonshine.ai). Accepts raw PCM audio over WebSocket and answers in Deepgram's live-transcription frames, so `stt.provider = "moonshine"` runs through the same accumulate-and-dedupe path the server already uses for Deepgram.

Everything runs on-device. No API key, no account — the model is downloaded on first run and cached.

## Models

The catalog picks a default per language; `--model-arch` overrides it.

| Arch | Notes |
|------|-------|
| `tiny`, `base` | Non-streaming, lowest footprint |
| `tiny-streaming`, `base-streaming` | Built for live audio |
| `small-streaming`, `medium-streaming` | Higher accuracy, more compute |

English models are MIT. Other languages load under the non-commercial [Moonshine Community License](https://www.moonshine.ai/license), and the server prints a notice when they do.

## Install

```bash
pip install -r requirements.txt
```

## Run

```bash
python server.py
# ws://127.0.0.1:8210

python server.py --port 9000 --model-arch base-streaming
python server.py --language es --update-interval 0.8
```

## Protocol

- **Client → Server**: binary WebSocket frames — raw PCM (16 kHz, 16-bit signed LE, mono)
- **Client → Server**: JSON text frames — `{"type": "KeepAlive" | "Finalize" | "CloseStream"}`
- **Server → Client**: Deepgram JSON frames — `Results`, `SpeechStarted`, `UtteranceEnd`, `Metadata`

```json
{"type":"SpeechStarted","channel":[0,1],"timestamp":0.42}
{"type":"Results","channel_index":[0,1],"start":0.42,"duration":0.9,"is_final":false,"speech_final":false,"channel":{"alternatives":[{"transcript":"turn on"}]}}
{"type":"Results","channel_index":[0,1],"start":0.42,"duration":1.8,"is_final":true,"speech_final":true,"channel":{"alternatives":[{"transcript":"turn on the lights"}]}}
{"type":"UtteranceEnd","channel":[0,1],"last_word_end":2.22}
```

Moonshine completes a transcript line only at the end of an utterance, so every `is_final` it sends is also a `speech_final`. Deepgram splits the two because it freezes words mid-sentence.

There is no confidence field: Moonshine does not report one, and the server sends no number the model did not produce. The Go client decodes the absent field as 0, which the pipeline reads as "unknown" rather than "low".

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `--host` | `127.0.0.1` | Bind host |
| `--port` | `8210` | Bind port |
| `--language` | `en` | Two-letter language code |
| `--model-arch` | catalog default | `tiny`, `base`, `tiny-streaming`, `base-streaming`, `small-streaming`, `medium-streaming` |
| `--update-interval` | `0.5` | Seconds of audio between transcription passes |
| `--log-level` | `INFO` | Logging level |

`--update-interval` is a floor, not a cadence. A pass has to cover at least as much audio as the last one took to produce, so a machine that cannot keep up lets passes grow instead of falling further behind on every one.
