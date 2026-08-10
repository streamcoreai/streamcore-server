**English** | [简体中文](./providers.zh-CN.md)

# Provider integrations

| Role | Providers | Required credentials |
|------|-----------|----------------------|
| STT | `deepgram`, `assemblyai`, `openai`, `vibevoice` | Deepgram API key, AssemblyAI API key, OpenAI API key, or a local VibeVoice ASR server |
| LLM | `openai`, `ollama` | OpenAI API key, or an Ollama instance you control |
| TTS | `cartesia`, `deepgram`, `elevenlabs`, `minimax`, `speechify`, `vibevoice` | Matching provider API key, or a local VibeVoice TTS server |
| Speech-to-speech | `grok` | xAI API key — replaces STT, LLM, and TTS together |
| RAG (optional) | `pgvector`, `supabase` | Postgres connection string or Supabase URL + key, plus an OpenAI key for embeddings |

Notes:

- `stt.provider = "openai"` uses Whisper-style final transcription instead of streaming partials.
- `llm.provider = "ollama"` targets any Ollama-compatible endpoint via `base_url` — local or on your own infrastructure.
- `stt.provider = "vibevoice"` and `tts.provider = "vibevoice"` use local models; start the Python sidecars first.
- `tts.provider = "minimax"` covers 40+ languages and is the strongest option for Mandarin. See [MiniMax TTS](#minimax-tts) for the region and model-plan caveats.
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
| Plugins, skills, vision, car control | Registered as function tools; the same handlers run in both modes |
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

## Local VibeVoice setup

VibeVoice provides fully local STT and TTS with no API keys, using [VibeVoice-ASR](https://huggingface.co/mlx-community/VibeVoice-ASR-4bit) for recognition and [VibeVoice-Realtime-0.5B](https://huggingface.co/mlx-community/VibeVoice-Realtime-0.5B-6bit) for synthesis via two lightweight Python sidecars. On Apple Silicon they use [mlx-audio](https://github.com/Blaizzy/mlx-audio) (MLX); on Linux/Windows they fall back to PyTorch automatically.

```bash
# Apple Silicon (MLX)
pip install mlx-audio numpy websockets fastapi uvicorn
# OR PyTorch (Linux / CUDA)
pip install torch transformers librosa numpy websockets fastapi uvicorn

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
