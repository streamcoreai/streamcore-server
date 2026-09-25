# Moonshine TTS Server

**English** | [简体中文](./README.zh-CN.md)

Text-to-speech server using [Moonshine](https://moonshine.ai). `/v1/speak` mirrors Deepgram's endpoint of the same name — text in the body, audio format in the query, raw headerless PCM written back as it is synthesized — so playback starts on the first clause instead of waiting for the whole utterance.

Everything runs on-device. No API key, no account — the voice is downloaded on first use and cached.

## Voices

Voice ids carry a vocoder prefix: `kokoro_`, `piper_`, or `zipvoice_`. `GET /v1/voices` lists what is on disk and what the catalog can fetch.

```bash
curl 'http://127.0.0.1:8310/v1/voices?language=en_us'
```

## Install

```bash
pip install -r requirements.txt
```

## Run

```bash
python server.py
# http://127.0.0.1:8310

python server.py --port 9000 --voice kokoro_am_adam
python server.py --language es --voice kokoro_ef_dora --preload
```

The first request for a voice downloads it, which can take a while. `--preload` moves that cost to startup.

## API

### `POST /v1/speak`

| Query param | Default | Description |
|-------------|---------|-------------|
| `voice` | `--voice` | Catalog voice id |
| `language` | `--language` | Synthesis language, e.g. `en_us` |
| `encoding` | `linear16` | Only `linear16` is supported |
| `sample_rate` | `16000` | Output rate; the model's own rate is resampled to it |
| `container` | `none` | Only `none` is supported |

The body is the text, either `text/plain` or Deepgram's `{"text": "..."}` JSON. The response is raw PCM (16-bit signed LE, mono) at `sample_rate`, streamed one chunk per piece of the reply.

```bash
curl -s -X POST 'http://127.0.0.1:8310/v1/speak?voice=kokoro_af_heart' \
  -H 'Content-Type: text/plain' \
  --data 'Hello from Moonshine.' --output speech.pcm
```

### `GET /v1/voices`

```json
{"language": "en_us", "present": ["kokoro_af_heart"], "downloadable": ["kokoro_af_bella", "..."]}
```

### `GET /health`

```json
{"status": "ok"}
```

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `--host` | `127.0.0.1` | Bind host |
| `--port` | `8310` | Bind port |
| `--language` | `en_us` | Default synthesis language |
| `--voice` | `kokoro_af_heart` | Default voice id |
| `--preload` | off | Load the default voice at startup rather than on first request |
| `--log-level` | `INFO` | Logging level |

## Notes

A synthesizer speaks one thing at a time — the native layer rejects a second generation while one is in flight — so requests are serialized. Measured on an M-series Mac with `kokoro_af_heart`: first chunk at ~130 ms, synthesis running about 9x faster than playback, which is well clear of what a live conversation needs.

Chunks are resampled with a running fractional position rather than one chunk at a time, so the joins between them do not click.
