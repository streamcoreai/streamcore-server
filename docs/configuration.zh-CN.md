[English](./configuration.md) | **简体中文**

# 配置参考

从 [`config.toml.example`](../config.toml.example) 开始（下面代码块中的注释与该文件保持一致，故保留英文）：

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

[agent]                              # 接入自有智能体：当 llm.provider = "agent" 时使用
url = ""                             # 你的端点，如 http://localhost:9000/agent。每一轮对话以 JSON POST 过去
api_key = ""                         # 以 Authorization: Bearer 发送。留空则不鉴权
timeout_ms = 60000                   # 单轮总预算，含流式返回回复的时间

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

说明：

- `server.public_ip` 加上 `server.turn_secret` 会启用内置的 Pion STUN/TURN 服务，取代外部 coturn 容器。TURN 监听 UDP 与 TCP 3478，并在 UDP 50001–60000 上中转媒体。
- `server.max_sessions` 用于限制分布式客户端的破坏半径：按 IP 的限流做不到这一点，而每个会话都在消耗 CPU 和服务商费用。超过上限后 `POST /whip` 返回 503 并带 `Retry-After`；会话恢复（resume）不受限制，因为它重新接入的会话已被计数。请按单实例实际能承载的量来设置。
- `plugins.directory` 是插件与技能加载的必要条件；不设置则跳过发现流程。
- `pipeline.barge_in` 允许用户在智能体说话时打断。用户一开口抢话，智能体音量立即压低；若判定只是回应词则恢复。
- `pipeline.greeting` 在会话连接时播放。存在 `pipeline.greeting_outgoing` 时，它用于 SIP 外呼。
- `pipeline.debug = true` 会通过 DataChannel 发出时延事件，并在日志中记录每轮的时延分解。
- `pipeline.turn_merge_ms` 是一条 final 转写被暂留多久，以便后续内容合并进同一轮。如果智能体总在用户话说到一半时抢答，就调高；如果回复显得迟钝，就调低。当文本结束在半句话或悬空词上时，等待会自动延长。
- `pipeline.user_speech_quiet_ms` 是用户需要安静多久，智能体才开始说话。
- `pipeline.rag_prefetch` 让检索与轮次合并窗口重叠。默认关闭；它会发出一次推测性的 embedding + 检索，若该轮文本发生变化则丢弃。
- `pipeline.readback_bargein_guard_enabled` 可避免弱纠正与回应词打断智能体的确认复述。只有明确的命令（stop、cancel、hang up）才会打断。默认关闭。
- `deepgram.endpointing` 与 `deepgram.utterance_end_ms` 调节上游认定一轮结束的时机；轮次合并去抖运行在它们之上。
- `deepgram.tts_model` 选择 Aura 音色；STT（`model`）与 TTS（`tts_model`）共用同一个 API key。音色命名规则为 `[family]-[voice]-[language]` —— 见 [Deepgram 音色列表](https://developers.deepgram.com/docs/tts-models)。
- `cartesia.max_concurrency` 应与你套餐的 TTS 并发上限一致 —— Cartesia 统计的是进行中的生成数而不是通话数，超限会返回 429。
- `minimax.base_url` 用于选择区域。留空即使用全球端点；中国大陆账号必须指向 `https://api.minimaxi.com/v1`，因为两个平台的 key 不通用。
- `minimax.model` 必须与你的套餐匹配：Token Plan 的 key（`sk-cp-`）只覆盖 `speech-2.8-hd`，其他模型走按量计费，余额为零时报错 `2056`。

## 用环境变量注入密钥

所有密钥都可以通过环境变量注入，而不必写进 `config.toml` —— 这正是容器和云部署所需要的：让密钥不进入镜像和文件。已设置的环境变量会**覆盖**文件中的值（部署环境比打包进去的配置更权威），每次覆盖都会在启动日志中按变量名记录（绝不记录值）。空的环境变量视为未设置。

服务商密钥沿用各服务商的惯例变量名；本服务自有的密钥使用 `STREAMCORE_` 前缀：

| 环境变量 | 覆盖的配置项 |
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

非密钥类设置（模型、音色、调优参数）仍然只放在 `config.toml` 中。

各服务商的具体行为与注意事项见[服务商](./providers.zh-CN.md)。
