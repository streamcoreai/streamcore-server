**English** | [简体中文](./providers.zh-CN.md)

# Provider integrations

| Role | Providers | Required credentials |
|------|-----------|----------------------|
| STT | `aliyun`, `assemblyai`, `deepgram`, `moonshine`, `openai`, `telnyx`, `vibevoice`, `volcengine` | Matching provider API key, or a local Moonshine / VibeVoice ASR server |
| LLM | `openai`, `ollama`, `agent` | OpenAI API key, an Ollama instance you control, or your own HTTP agent endpoint |
| TTS | `cartesia`, `deepgram`, `elevenlabs`, `mimo`, `minimax`, `moonshine`, `speechify`, `telnyx`, `vibevoice` | Matching provider API key, or a local Moonshine / VibeVoice TTS server |
| Speech-to-speech | `grok` | xAI API key — replaces STT, LLM, and TTS together |
| RAG (optional) | `pgvector`, `supabase` | Postgres connection string or Supabase URL + key, plus an OpenAI key for embeddings |

Notes:

- `stt.provider = "openai"` uses batch final transcription instead of streaming partials, so live captions show finals only and barge-in runs in a degraded mode: the interrupt fires on VAD alone once the full 600ms backchannel window has elapsed, since there is no text to classify a short burst with. Choose `whisper-1`, `gpt-4o-transcribe`, or `gpt-4o-mini-transcribe` with `openai.stt_model`.
- `stt.provider = "telnyx"` fronts Telnyx's in-house and a dozen hosted transcription engines over one WebSocket and one key. `transcription_engine` defaults to `Deepgram`, so barge-in and live captions work out of the box; the in-house `Telnyx` engine is finals-only, so live captions show finals only and barge-in waits out the backchannel window on VAD alone. See [Telnyx STT](#telnyx-stt).
- `llm.provider = "ollama"` targets any Ollama-compatible endpoint via `base_url` — local or on your own infrastructure.
- `llm.provider = "agent"` POSTs each turn to an HTTP endpoint you host; your agent owns memory, prompting, and tools, and replies stream back as SSE, chunked text, or JSON. See [Bring your own agent](./bring-your-own-agent.md).
- `stt.provider = "vibevoice"` and `tts.provider = "vibevoice"` use local models; start the Python sidecars first.
- `stt.provider = "moonshine"` and `tts.provider = "moonshine"` are the other fully local pair, also behind Python sidecars. The STT sidecar answers in Deepgram's wire format, so it runs through the same transcript handling as Deepgram itself. See [Local Moonshine setup](#local-moonshine-setup).
- `tts.provider = "minimax"` covers 40+ languages and is the strongest option for Mandarin. See [MiniMax TTS](#minimax-tts) for the region and model-plan caveats.
- `tts.provider = "telnyx"` is Telnyx hosted synthesis over a per-utterance WebSocket; voice availability varies by account. See [Telnyx TTS](#telnyx-tts) for the connection model and the voice catalog.
- `tts.provider = "mimo"` is Xiaomi's MiMo TTS, with Chinese and English voices and optional voice cloning on the paid models.
- `stt.provider = "aliyun"` is Alibaba Cloud Model Studio (DashScope) streaming ASR; `vocabulary_id` biases it toward domain terms.
- `stt.provider = "volcengine"` is Doubao streaming ASR — useful where Deepgram is slow to reach or its Mandarin is not good enough. The console gives a free hourly allowance.
- `realtime.provider = "grok"` switches to speech-to-speech and ignores `[stt]`, `[llm]`, and `[tts]` entirely.

Every key and knob lives in the [configuration reference](./configuration.md).

## Speech-to-speech (Grok Voice)

Setting `realtime.provider` swaps the three-stage STT → LLM → TTS chain for a single model that takes caller audio and answers with audio. Transcription, reasoning, and synthesis happen in one hop, which removes the two handoffs that dominate turn latency in the classic pipeline.

```toml
[realtime]
provider = "grok"

[grok]
api_key = "xai-..."
model = "grok-voice-think-fast-2.0"   # or "grok-voice-latest"
voice = "eve"
reasoning_effort = "high"             # "none" trades nuance for latency
system_prompt = "You are a helpful assistant on a phone call. Keep it short."
```

The pipeline keeps the same Opus/RTP path at both ends and runs a single `runRealtime` loop between them, in place of `runInbound` + `runAgent`. Audio is negotiated as 16 kHz PCM over binary WebSocket frames — the pipeline's native rate, so nothing resamples and nothing is base64-encoded on the audio path.

### Models

| `model` | Notes | Cost |
|---|---|---|
| `grok-voice-think-fast-2.0` | Newest and most capable. Reasoning on by default | $0.08 / min ($4.80 / hr) |
| `grok-voice-think-fast-1.0` | Previous generation, cheaper | $0.05 / min ($3.00 / hr) |
| `grok-voice-latest` | Alias that always points at the newest model — currently `grok-voice-think-fast-2.0` | Tracks whichever model it resolves to |

Both models also bill $0.004 per text input. Pin a versioned name in production: `grok-voice-latest` re-points when xAI ships a new model, changing behaviour and price under a running deployment.

Two things to know when writing the prompt for these models:

- **Keep `system_prompt` short.** These are strong enough that porting a long GPT-era prompt over verbatim makes them worse. xAI's own advice is to strip out workaround prompting and edge-case patches written for weaker models.
- **Reasoning is on by default.** `reasoning_effort = "high"` helps with multi-step instructions, nuanced tone, and ambiguous questions. Set `"none"` for lower latency when the agent's job is simple.

The model has no idea what product it is deployed in — if you want it to identify itself ("you are the StreamCore assistant"), that belongs in `system_prompt`.

### Voices

`voice` takes a lowercase built-in voice ID (default `eve`) or a custom voice ID cloned via xAI's Custom Voices API. Fetch the current roster with `GET /v1/tts/voices`. The same voices serve the TTS API, so anything in xAI's TTS voice table works here.

### What changes in this mode

| Capability | Behaviour |
|---|---|
| Turn detection and barge-in | Owned entirely by the model's server-side VAD. See below |
| Plugins, skills, vision, movement control | Registered as function tools; the same handlers run in both modes |
| RAG | Exposed as a `knowledge_search` tool the model calls on demand, rather than being injected into every prompt |
| Hosted search | `web_search` and `x_search` run on xAI's side with no local plugin |
| Rolling summary, misunderstanding detection | Not used — these operate on STT transcripts the model never emits |
| Delivery tags (`[warm]`, `[calm]`) | Not used — the model controls its own prosody |
| Plugin `thinking_sound` | Not played — it would interleave with model audio still draining from the outbound queue. Logged once per call |

### Barge-in and turn detection

The model detects interruptions, not the server. Grok's VAD decides the caller has cut in, stops generating, and sends `input_audio_buffer.speech_started`; the server's only job is to discard audio it has already buffered locally, since the model cannot un-send frames that are already queued here.

This means **`[pipeline] barge_in` has no effect in realtime mode.** It is read only by `runInbound`, which does not run. So do the local energy VAD, the backchannel suppression window, `readback_bargein_guard_enabled`, and audio ducking — barge-in is a hard cut here rather than a duck-and-recover. Leave `barge_in = true` anyway so the setting is correct if you switch back to the classic pipeline.

Tuning moves to `[grok]`:

| Setting | Use it when |
|---|---|
| `vad_threshold` (0.1–0.9, default 0.85) | Noise, coughs, or "mm-hm" cut the agent off. Raise it. This is the closest replacement for the backchannel suppression that classic mode does in software |
| `silence_duration_ms` | Callers get cut off mid-sentence. Raise it to allow longer pauses |
| `prefix_padding_ms` (default 333) | The first word of a turn gets clipped. Raise it |
| `idle_timeout_ms` | You want the agent to re-engage after silence. Unset disables the check-in |

There is no way to keep automatic turn-taking while disabling interruption: `turn_detection` is either `server_vad` or `null`, and `null` means the server must decide when every turn ends and explicitly request each response. Tune the thresholds instead.

### Transcripts

`transcription = true` runs a separate transcription pass purely so clients receive `transcript` events for display — the model itself hears the audio directly and does not need it. Turn it off to skip the cost if your client shows no transcript.

These transcripts are cumulative and arrive in fragments: an update may revise words it already emitted, and a caller who pauses mid-sentence produces several finalised fragments for one question. The server merges them into a single turn and commits it when the model starts responding, so one spoken turn renders as one message. Set `[pipeline] debug = true` to log every provider event with its transcript payload.

### Cost

Billing is per minute of wall-clock audio rather than per token, which changes the economics against a self-assembled pipeline — idle time on an open call still bills, so `idle_timeout_ms` and prompt call teardown matter more here than in classic mode. See the model table above for rates.

## MiniMax TTS

MiniMax's T2A v2 API over SSE, covering 40+ languages with a strong Mandarin voice set. It is the one hosted provider that emits PCM at whatever sample rate you ask for, so the server requests 16 kHz mono — the pipeline's native rate — and nothing is resampled on the way to the encoder.

```toml
[tts]
provider = "minimax"

[minimax]
api_key = ""
voice_id = "English_Graceful_Lady"   # or "Chinese (Mandarin)_News_Anchor"
model = "speech-2.6-turbo"
# base_url = "https://api.minimax.io/v1"
```

Three things to get right:

- **Region.** `base_url` defaults to the global endpoint `https://api.minimax.io/v1`. Accounts registered on the mainland-China platform must set `https://api.minimaxi.com/v1` instead — keys are not interchangeable between the two, and using the wrong host fails auth rather than falling back.
- **Model vs. plan.** `speech-2.6-turbo` is the low-latency tier and the right default on a live audio path; the `-hd` models sound better but add hundreds of milliseconds. A Token Plan key (`sk-cp-`) covers only `speech-2.8-hd` — any other model routes to pay-as-you-go and fails with error `2056` on a zero balance.
- **Errors arrive as HTTP 200.** MiniMax reports auth and quota failures in a `base_resp.status_code` field inside a 200 response. The client checks it, so these surface as real errors instead of silent empty audio.

Delivery tags map onto MiniMax's emotion enum: `[warm]` and `[excited]` become `happy`, `[calm]` and `[empathetic]` become `calm`. `[empathetic]` deliberately lands on `calm` rather than `sad`, which overshoots into sounding upset on apologies and bad news. Tags with no emotion mapping still take effect through speed, which is clamped to MiniMax's 0.5–2.0 range.

## Telnyx TTS

Telnyx hosted speech synthesis over WebSocket, streaming linear16 at the pipeline's native 16 kHz so the audio path never resamples.

```toml
[tts]
provider = "telnyx"

[telnyx]
api_key = ""
voice = "Telnyx.Qwen3TTS.d9348e0d-988a-42cc-a64e-18093fe45c03"
voice_speed = 1.0
```

Three things to know:

- **One WebSocket per utterance.** The protocol has no per-utterance completion marker; `isFinal` arrives only after the client sends an empty-text teardown, so the client dials a fresh connection for each utterance: init, text, teardown, collect audio until the final frame, server closes. Measured against a persistent connection this costs nothing on the live path: synthesis outpaces playback (~2.4x) and first audio arrives well under a second after dial.
- **Voices are per-account.** `voice` is any catalog name from `GET /v2/text-to-speech/voices`, and availability varies by account; a voice your key is not provisioned for fails the WebSocket handshake with HTTP 403 rather than erroring mid-call. The config default (`Telnyx.Qwen3TTS.d9348e0d-988a-42cc-a64e-18093fe45c03`) is a verified Qwen3TTS voice (Delta, female in the catalog); not a guarantee for every key.
- **Telnyx LLMs need no new provider.** `openai.base_url = "https://api.telnyx.com/v2/ai"` points the existing `openai` LLM provider at Telnyx inference (e.g. model `glm-5.3`) with zero code.

Delivery tags map onto `voice_speed` (clamped to 0.8–1.2, the same conversational band as Cartesia), and `voice_speed` in config sets the baseline pace for untagged sentences.

## Telnyx STT

Streaming transcription over the Telnyx speech-to-text WebSocket: raw linear16 binary frames in, JSON transcript frames out, at the pipeline's native 16 kHz mono so nothing resamples. The same `[telnyx]` section and API key as TTS cover both roles; `transcription_engine` picks the recognizer.

```toml
[stt]
provider = "telnyx"

[telnyx]
api_key = ""
transcription_engine = "Deepgram"   # verified: "Deepgram" (partial results, barge-in works) or "Telnyx" (in-house, finals-only). Case matters
```

Three things to know:

- **`Deepgram` is the default.** It streams interim results exactly like the built-in Deepgram provider, so barge-in and live captions work out of the box. A finals-only default would silently degrade both for anyone who just sets the provider and starts talking: barge-in to VAD-only, live captions to finals only.
- **The in-house `Telnyx` engine is finals-only.** It emits exactly one final per utterance, only after the caller stops speaking: no interims, no timestamps, confidence `null`. Live captions therefore show nothing until the final lands. Barge-in cannot confirm an interruption from partial text, so it degrades instead of switching off: speech over the agent opens the suppression window on VAD alone, and the interrupt fires once the speech has lasted past the full 600ms backchannel window. A burst that ends inside the window is treated as backchannel — with no text, it cannot be told apart from "mm-hm" — so sustained interruptions work and quick ones do not. Because the engine answers only after audio stops, the client runs its own endpointing (`internal/vad`, the same detector the pipeline uses, with the silence timeout `openai.go` uses) and opens one socket per utterance, closing it once the final lands, since the server keeps it open. A startup log line says the session runs barge-in VAD-only on this engine, so the operator learns it from the log. See the [design discussion](https://github.com/streamcoreai/streamcore-server/issues/75) for the trade-offs.
- **The engine name is case-sensitive and sent verbatim.** `telnyx` is rejected with a structured error frame that lists the supported engines. Only `Deepgram` and `Telnyx` are verified here; the other hosted engines the endpoint fronts (AssemblyAI, Azure, and the rest of that list) pass through untested.

Confidence arrives as `null` from the in-house engine and a 0-1 float from hosted ones; the pipeline treats `null` as unknown rather than low.

## Local VibeVoice setup

VibeVoice provides fully local STT and TTS with no API keys, using [VibeVoice-ASR](https://huggingface.co/mlx-community/VibeVoice-ASR-4bit) for recognition and [VibeVoice-Realtime-0.5B](https://huggingface.co/mlx-community/VibeVoice-Realtime-0.5B-6bit) for synthesis via two lightweight Python sidecars. On Apple Silicon they use [mlx-audio](https://github.com/Blaizzy/mlx-audio) (MLX); on Linux/Windows they fall back to PyTorch automatically.

```bash
# Apple Silicon (MLX)
pip install mlx-audio numpy websockets onnxruntime requests fastapi uvicorn
# OR PyTorch (Linux / CUDA)
pip install torch "transformers>=5.3.0" accelerate librosa numpy websockets onnxruntime requests fastapi uvicorn

python external/vibeVoice/vibeVoiceAsr/server.py   # ws://127.0.0.1:8200
python external/vibeVoice/vibeVoiceTTS/server.py   # http://127.0.0.1:8300
```

```toml
[stt]
provider = "vibevoice"

[tts]
provider = "vibevoice"

[vibevoice]
asr_url = "ws://127.0.0.1:8200"
tts_url = "http://127.0.0.1:8300"
voice = "en-Emma_woman"
```

The ASR server accepts live PCM over WebSocket and emits JSON transcript events. The TTS server accepts HTTP POST and returns raw PCM.

## Local Moonshine setup

[Moonshine](https://moonshine.ai) is the other fully local pair: streaming STT and TTS from one pip package, no API key and no account. Models are downloaded on first run and cached. English STT models are MIT; other languages load under the non-commercial [Moonshine Community License](https://www.moonshine.ai/license).

```bash
pip install -r external/moonshine/moonshineStt/requirements.txt
pip install -r external/moonshine/moonshineTts/requirements.txt

python external/moonshine/moonshineStt/server.py   # ws://127.0.0.1:8210
python external/moonshine/moonshineTts/server.py   # http://127.0.0.1:8310
```

```toml
[stt]
provider = "moonshine"

[tts]
provider = "moonshine"

[moonshine]
stt_url = "ws://127.0.0.1:8210"
tts_url = "http://127.0.0.1:8310"
voice = "kokoro_af_heart"
```

Language and model selection are sidecar flags rather than config keys, because both are fixed when the model loads rather than chosen per request: `--language`, `--model-arch` for STT, `--language` and `--voice` for TTS. English resolves to `medium-streaming` by default; `--model-arch tiny-streaming` trades accuracy for a much smaller footprint. The first request for a TTS voice downloads it, so `--preload` is worth setting for anything but a first try.

Both sidecars speak Deepgram's wire format. The STT server sends `Results`, `SpeechStarted` and `UtteranceEnd` frames, which the server decodes into Deepgram's own types and routes through the same accumulator — so overlapping finals are merged and an immediate repeat is suppressed exactly as they are for Deepgram. It reports no confidence, and the frames leave the field out rather than inventing one; the pipeline reads the absent value as unknown, not as low. The TTS server mirrors `/v1/speak`, writing raw headerless PCM as it synthesizes, so playback starts on the first clause.

Measured on an M-series Mac: first TTS chunk at ~130ms with synthesis running about 9x faster than playback.
