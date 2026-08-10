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

各服务商的具体行为与注意事项见[服务商](./providers.zh-CN.md)。
