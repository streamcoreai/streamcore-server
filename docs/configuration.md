**English** | [简体中文](./configuration.zh-CN.md)

# Configuration reference

Start from [`config.toml.example`](../config.toml.example):

```toml
[server]
port = "8080"
# public_ip = ""       # Public IP for ICE candidates (e.g. EC2 Elastic IP); enables built-in STUN/TURN
# turn_secret = ""     # Shared secret for the built-in STUN/TURN server (required when public_ip is set)
# jwt_secret = ""      # Enables JWT auth on /whip and the POST /token endpoint
# api_key = ""         # Required to call POST /token when set
# session_grace_ms = 30000  # Grace period before a session with no peers is reaped (allows ICE restart / redial)
# max_sessions = 0     # Global cap on live sessions; past it POST /whip returns 503 + Retry-After. 0 = unlimited

[plugins]
directory = "./plugins"

[pipeline]
barge_in = true
greeting = ""
greeting_outgoing = ""
debug = false
user_speech_quiet_ms = 600           # Quiet period after the caller stops before the agent speaks
turn_merge_ms = 350                  # Debounce window for merging finals into one turn
# rag_prefetch = false               # Start retrieval during the merge window instead of after it
# readback_bargein_guard_enabled = false  # Ignore weak barge-ins while the agent reads values back

# Speech-to-speech. When set, replaces [stt], [llm], and [tts] entirely.
[realtime]
provider = ""                        # "grok", or empty for the classic pipeline

[stt]
provider = "deepgram"

[llm]
provider = "openai"

[tts]
provider = "cartesia"

# [grok]                             # Used when realtime.provider = "grok"
# api_key = ""
# model = "grok-voice-latest"        # Pin a version in production, e.g. grok-voice-think-fast-2.0
# voice = "eve"
# reasoning_effort = "high"          # "none" trades nuance for latency
# system_prompt = ""                 # Keep short; long GPT-era prompts hurt these models
# silence_duration_ms = 500          # Silence before the caller's turn ends
# vad_threshold = 0.85               # 0.1-0.9; higher demands louder audio to trigger a turn
# transcription = true               # Client-facing transcript only; the model hears audio directly
# web_search = false                 # xAI-hosted search, no local plugin needed

[deepgram]
api_key = ""
model = "nova-3"
tts_model = "aura-2-thalia-en"       # Aura voice when tts.provider = "deepgram"; aura-2-theia-en is Australian feminine
# language = ""                      # BCP-47 tag (en-US, es-MX); non-en/es routes to the multilingual model
endpointing = "300"                  # Silence (ms) before a transcript is finalised
utterance_end_ms = "1000"            # Silence (ms) before UtteranceEnd; flushes a turn with no speech_final
# keyterms = ["Tauranga", "BYD"]     # Nova-3 only: bias the decoder toward domain vocabulary

# [assemblyai]                       # Alternative streaming STT provider
# api_key = ""
# model = "u3-rt-pro"                # or "u3-rt" for the cheaper baseline
# language = ""                      # BCP-47; region is stripped (en-NZ -> en). Empty auto-detects
# format_turns = true                # Auto-punctuate and capitalise the final turn
# end_of_turn_silence_ms = 0         # Override how long the model waits before ending a turn
# keyterms = []

[openai]
api_key = ""
model = "gpt-4o-mini"
system_prompt = "You are a helpful AI voice assistant. Keep your responses concise and conversational."

[ollama]
base_url = "http://localhost:11434"
model = "gpt-oss:20b"
system_prompt = "You are a helpful AI voice assistant. Keep your responses concise and conversational."

[agent]                              # Bring your own agent: used when llm.provider = "agent"
url = ""                             # Your endpoint, e.g. http://localhost:9000/agent. Each turn is POSTed as JSON
api_key = ""                         # Sent as Authorization: Bearer. Empty disables auth
timeout_ms = 60000                   # Whole-turn budget, including streaming the reply

[cartesia]
api_key = ""
voice_id = ""
max_concurrency = 3                  # Generations in flight before requests queue locally instead of 429ing
# ws_url = ""                        # Defaults to wss://api.cartesia.ai/tts/websocket

[elevenlabs]
api_key = ""
voice_id = ""
model = ""

[speechify]
api_key = ""
voice_id = ""
model = ""

[minimax]
api_key = ""
voice_id = ""                        # Defaults to English_Graceful_Lady; 40+ languages available
model = ""                           # Defaults to speech-2.6-turbo (low latency)
# base_url = ""                      # Defaults to https://api.minimax.io/v1; mainland-China accounts use https://api.minimaxi.com/v1

[vibevoice]
asr_url = "ws://127.0.0.1:8200"
tts_url = "http://127.0.0.1:8300"
voice = "en-Emma_woman"

# RAG is optional — omit the [rag] section to disable it entirely.
# [rag]
# provider = "supabase"       # "pgvector" or "supabase"
# top_k = 3
# embedding_model = "text-embedding-3-small"

# [pgvector]
# connection_string = "postgres://user:pass@localhost:5432/mydb"
# table = "documents"

# [supabase]
# url = "https://xxx.supabase.co"
# api_key = ""
# function = "match_documents"
# table = "documents"
```

Notes:

- `server.public_ip` plus `server.turn_secret` enables the built-in Pion STUN/TURN server, replacing an external coturn container. TURN listens on UDP and TCP 3478 and relays media on UDP 50001–60000.
- `server.max_sessions` bounds the blast radius of distributed clients: the per-IP rate limit cannot, and every session burns CPU and provider spend. Past the cap, `POST /whip` returns 503 with `Retry-After`; session resumes are exempt, since they reattach to a session that is already counted. Size it to what one instance can actually serve.
- `plugins.directory` is required for plugins and skills to load; omit it and discovery is skipped.
- `pipeline.barge_in` lets users interrupt the agent while it is speaking. Agent audio ducks as soon as the caller starts talking over it and recovers if the interruption turns out to be a backchannel.
- `pipeline.greeting` plays when a session connects. `pipeline.greeting_outgoing` is used for outbound SIP calls when present.
- `pipeline.debug = true` emits timing events over the DataChannel and logs a per-turn latency breakdown.
- `pipeline.turn_merge_ms` is how long a final transcript is held so a continuation can merge into the same turn. Raise it if the agent answers callers halfway through a sentence; lower it if replies feel sluggish. The wait extends automatically when the text ends mid-dictation or on a dangling word.
- `pipeline.user_speech_quiet_ms` is how long the caller must be quiet before the agent starts speaking.
- `pipeline.rag_prefetch` overlaps retrieval with the turn-merge window. Off by default; it issues a speculative embedding + search that is discarded if the turn text changes.
- `pipeline.readback_bargein_guard_enabled` keeps weak corrections and backchannels from cutting off a confirmation readback. Only explicit commands (stop, cancel, hang up) interrupt. Off by default.
- `deepgram.endpointing` and `deepgram.utterance_end_ms` tune when a turn is considered finished upstream; the turn-merge debounce runs on top of them.
- `deepgram.tts_model` picks the Aura voice; STT (`model`) and TTS (`tts_model`) share the one API key. Voices are named `[family]-[voice]-[language]` — see [Deepgram's voice list](https://developers.deepgram.com/docs/tts-models).
- `cartesia.max_concurrency` should match your plan's TTS concurrency limit — Cartesia counts active generations, not calls, and returns 429 past the limit.
- `minimax.base_url` selects the region. Leave it unset for the global endpoint; mainland-China accounts must point it at `https://api.minimaxi.com/v1`, since keys do not work across the two platforms.
- `minimax.model` must match your plan: a Token Plan key (`sk-cp-`) only covers `speech-2.8-hd`, while any other model bills pay-as-you-go and errors with `2056` on a zero balance.

## Secrets from environment variables

Every secret can be injected as an environment variable instead of written into `config.toml`, which is what container and cloud deployments need to keep keys out of images and files. A set variable **overrides** the file value — the deployment environment is more authoritative than a baked-in config — and each override is logged by name (never by value) at startup. An empty variable is treated as unset.

Provider keys use each provider's conventional variable name; secrets owned by this server use a `STREAMCORE_` prefix:

| Environment variable | Overrides |
|---|---|
| `STREAMCORE_TURN_SECRET` | `server.turn_secret` |
| `STREAMCORE_JWT_SECRET` | `server.jwt_secret` |
| `STREAMCORE_API_KEY` | `server.api_key` |
| `STREAMCORE_AGENT_API_KEY` | `agent.api_key` |
| `DEEPGRAM_API_KEY` | `deepgram.api_key` |
| `ASSEMBLYAI_API_KEY` | `assemblyai.api_key` |
| `OPENAI_API_KEY` | `openai.api_key` |
| `XAI_API_KEY` | `grok.api_key` |
| `CARTESIA_API_KEY` | `cartesia.api_key` |
| `ELEVENLABS_API_KEY` | `elevenlabs.api_key` |
| `SPEECHIFY_API_KEY` | `speechify.api_key` |
| `MINIMAX_API_KEY` | `minimax.api_key` |
| `MIMO_API_KEY` | `mimo.api_key` |
| `SUPABASE_API_KEY` | `supabase.api_key` |
| `PGVECTOR_CONNECTION_STRING` | `pgvector.connection_string` |

Non-secret settings (models, voices, tunables) stay in `config.toml` only.

Provider-specific behaviour and caveats: [Providers](./providers.md).
