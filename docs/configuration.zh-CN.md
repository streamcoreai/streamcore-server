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

# 各插件自己的配置，按插件清单里的 name 索引。
# [plugins.config.github]
# enabled = true

# Developer agent: GitHub App + the Codex harness. Both disabled by default.
[github]
enabled = false
app_id = ""                 # App ID or client ID; used as the JWT `iss`
installation_id = ""        # From the installation URL, or GET /repos/{owner}/{repo}/installation
private_key_path = ""       # PEM downloaded when the app was created
repositories = []           # Allowlist, as owner/name
# api_base_url = ""         # GitHub Enterprise Server API root

# Codex authenticates with your ChatGPT subscription. There is no api_key here.
[codex]
enabled = false
binary = "codex"
model_provider = "openai"
model = "gpt-5.6-terra"
workspace_root = "/var/lib/streamcore/codex"
turn_timeout_ms = 600000
# network_access = false    # Open the task sandbox to the network

[pipeline]
barge_in = true
greeting = ""
greeting_outgoing = ""
debug = false
user_speech_quiet_ms = 600           # Quiet period after the caller stops before the agent speaks
turn_merge_ms = 350                  # Debounce window for merging finals into one turn
# rag_prefetch = false               # Start retrieval during the merge window instead of after it
# readback_bargein_guard_enabled = false  # Ignore weak barge-ins while the agent reads values back
# echo_guard = "auto"                # auto | always | off. Auto follows each client's aec hint
# echo_guard_gain = 0.6              # Echo cannot exceed this fraction of what produced it
# echo_guard_margin = 1.8            # How far inbound must clear the echo bound to count as the caller
# echo_guard_window_ms = 400         # How long sent audio stays in the reference window

# Speech-to-speech. When set, replaces [stt], [llm], and [tts] entirely.
[realtime]
provider = ""                        # "grok", or empty for the classic pipeline

[stt]
provider = "deepgram"                # aliyun | assemblyai | deepgram | moonshine | openai | telnyx | vibevoice | volcengine

[llm]
provider = "openai"                  # openai | ollama | agent

[tts]
provider = "cartesia"                # cartesia | deepgram | elevenlabs | mimo | minimax | moonshine | speechify | telnyx | vibevoice

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

# [aliyun]                           # Alibaba Cloud Model Studio (DashScope) streaming ASR
# api_key = ""
# model = ""                         # Defaults to paraformer-realtime-v2; fun-asr-realtime is the alternative
# language = ""                      # Hint for a multilingual model ("zh", "en"); empty auto-detects
# vocabulary_id = ""                 # Hotword list from the console, for terms it keeps getting wrong
# url = ""                           # Defaults to wss://dashscope.aliyuncs.com/api-ws/v1/inference

# [volcengine]                       # Doubao streaming ASR
# api_key = ""                       # Console API key, sent as X-Api-Key; app-id + access-token is rejected
# resource_id = ""                   # Defaults to volc.seedasr.sauc.duration (hourly); .concurrent bills by concurrency
# model = ""                         # Defaults to bigmodel
# end_window_ms = 0                  # Silence that settles an utterance, defaults to 800. Same role as endpointing

[openai]
api_key = ""
model = "gpt-4o-mini"
stt_model = "whisper-1"             # whisper-1 | gpt-4o-transcribe | gpt-4o-mini-transcribe
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

[telnyx]                             # Telnyx 托管语音，当 tts.provider = "telnyx" 或 stt.provider = "telnyx" 时使用；一个 key 覆盖两个方向
api_key = ""
voice = "Telnyx.Qwen3TTS.d9348e0d-988a-42cc-a64e-18093fe45c03"        # GET /v2/text-to-speech/voices 目录中的任意音色；可用性因账号而异
voice_speed = 1.0                    # 播放速率倍数，限制在 0.8-1.2
transcription_engine = "Deepgram"    # STT 引擎，已验证取值："Deepgram"（有中间结果，打断可用；默认值）或
                                     # "Telnyx"（自研，只出最终结果：实时字幕只显示最终结果，打断降级为等满回应窗口后仅凭 VAD 触发；启动日志会写明）。大小写敏感，原样透传

[minimax]
api_key = ""
voice_id = ""                        # Defaults to English_Graceful_Lady; 40+ languages available
model = ""                           # Defaults to speech-2.6-turbo (low latency)
# base_url = ""                      # Defaults to https://api.minimax.io/v1; mainland-China accounts use https://api.minimaxi.com/v1

[vibevoice]
asr_url = "ws://127.0.0.1:8200"
tts_url = "http://127.0.0.1:8300"
voice = "en-Emma_woman"

[moonshine]
stt_url = "ws://127.0.0.1:8210"
tts_url = "http://127.0.0.1:8310"
voice = "kokoro_af_heart"            # Prefix picks the vocoder: kokoro_, piper_, or zipvoice_

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
- `plugins.config.<name>` 会在启动时交给对应插件，因此插件的凭证放在这个文件里，而不是放在源码旁边的 dotenv 中。名字里含点号时需要加引号：`[plugins.config."weather.get"]`。其中两个键由服务端自己读取而不透传——`enabled` 可以在不删除插件的前提下关掉它，`timeout_ms` 覆盖清单里的设置。
- `display.*`、`github.*` 与 `codex.*` 配置的功能现在都以插件形式发布——分别是 `display-projector`、`github` 和 `codex`。服务端会把每个段落转发给对应插件，因此现有部署无需改动即可继续工作；迁移时显式写出的 `[plugins.config.<name>]` 优先。GitHub 与 Codex 是两个独立插件，各自独立启用，这也正是这两个配置段一直以来的含义。
- `github.*` 启用 GitHub App 集成：CI 失败排查、仓库读取，以及需确认的 Pull Request 创建。它需要 App 私钥，而不是个人访问令牌。见[开发者智能体](./developer-agent.zh-CN.md)。
- `github.repositories` 是白名单。仓库必须**同时**列在其中且 App 安装可以访问，任何一项单独成立都不够。
- `codex.*` 启用 Codex 开发者智能体。Codex 通过官方登录流程使用你的 ChatGPT 订阅认证 —— 没有 API key 配置项，也永远不会使用 `OPENAI_API_KEY`。
- `codex.model_provider` 与 `codex.model` 会固定写在 Codex 命令行上。否则 `~/.codex/config.toml` 里指向其他供应商的默认值会悄悄接管，你的 ChatGPT 套餐将完全不会被使用。
- `codex.workspace_root` 是 Codex 唯一可以写入的位置。每个开发任务在其下获得独立的 git worktree；服务器自身的工作副本永远不在其中。
- `pipeline.barge_in` 允许用户在智能体说话时打断。用户一开口抢话，智能体音量立即压低；若判定只是回应词则恢复。
- `pipeline.greeting` 在会话连接时播放。存在 `pipeline.greeting_outgoing` 时，它用于 SIP 外呼。
- `pipeline.debug = true` 会通过 DataChannel 发出时延事件，并在日志中记录每轮的时延分解。
- `pipeline.turn_merge_ms` 是一条 final 转写被暂留多久，以便后续内容合并进同一轮。如果智能体总在用户话说到一半时抢答，就调高；如果回复显得迟钝，就调低。当文本结束在半句话或悬空词上时，等待会自动延长。
- `pipeline.user_speech_quiet_ms` 是用户需要安静多久，智能体才开始说话。
- `pipeline.rag_prefetch` 让检索与轮次合并窗口重叠。默认关闭；它会发出一次推测性的 embedding + 检索，若该轮文本发生变化则丢弃。
- `pipeline.readback_bargein_guard_enabled` 可避免弱纠正与回应词打断智能体的确认复述。只有明确的命令（stop、cancel、hang up）才会打断。默认关闭。
- `pipeline.echo_guard` 防止智能体被自己的声音打断。浏览器会在音频到达服务器之前先做 AEC，因此 VAD 根本看不到智能体自己的输出绕回来；而在电话线路上整条链路没有任何 AEC，回声虽然衰减了，但结构上和语音完全一样，单靠能量判据无法与真人区分。该开关会维护一个滚动窗口，记录服务器实际发出音频的 RMS，只有当上行音频超过 `已发送 RMS x echo_guard_gain x echo_guard_margin` 时才算作打断。智能体沉默时该下限为零，判定重新交回自适应阈值，因此干净线路上说话轻的来电者不受影响。

  这个判断是按会话而不是按服务器做的：同一个实例通常同时服务浏览器和 SIP 通话，而两者需要相反的答案。客户端通过在 WHIP URL 上加 `aec=none` 来声明自己处在没有 AEC 的链路上，`sip-server` 每通电话都会带上它；浏览器什么都不发，会被视为已经做过回声消除。

  | `echo_guard` | 行为 |
  | --- | --- |
  | `"auto"`（默认） | 对发送了 `aec=none` 的会话开启，其余关闭 |
  | `"always"` | 对所有会话开启。用于无法改动、又发不出该提示的裸链路客户端 |
  | `"off"` | 始终关闭 |

  除非你有一个无法修改、又跑在无 AEC 链路上的客户端，否则请保持 `"auto"`。在同时服务浏览器的服务器上设为 `"always"`，会让浏览器端真实的插话必须先盖过智能体自身的输出电平，而按会话判断的默认值正是为了避免这种回退。
- `pipeline.echo_guard_gain`、`pipeline.echo_guard_margin` 和 `pipeline.echo_guard_window_ms` 用于调节这个下限（仅对开启了该判定的会话生效）。gain 是回声相对于产生它的音频最多能有多响，margin 用于区分双讲与回声，window 应覆盖线路回声的往返时间。默认值（0.6、1.8、400ms）是在 8kHz µ-law 链路上实测得到的；只有拿到你自己链路的录音再去重新调参。该下限会自动跟随打断时的音量压低，因为它采样的是经过衰减后真正发到线路上的信号。
- `deepgram.endpointing` 与 `deepgram.utterance_end_ms` 调节上游认定一轮结束的时机；轮次合并去抖运行在它们之上。
- `deepgram.tts_model` 选择 Aura 音色；STT（`model`）与 TTS（`tts_model`）共用同一个 API key。音色命名规则为 `[family]-[voice]-[language]` —— 见 [Deepgram 音色列表](https://developers.deepgram.com/docs/tts-models)。
- `openai.stt_model` 独立于对话 `model` 选择批量转写模型，默认值为 `whisper-1`。
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
| `ALIYUN_API_KEY` | `aliyun.api_key` |
| `VOLCENGINE_API_KEY` | `volcengine.api_key` |
| `OPENAI_API_KEY` | `openai.api_key` |
| `XAI_API_KEY` | `grok.api_key` |
| `CARTESIA_API_KEY` | `cartesia.api_key` |
| `ELEVENLABS_API_KEY` | `elevenlabs.api_key` |
| `SPEECHIFY_API_KEY` | `speechify.api_key` |
| `TELNYX_API_KEY` | `telnyx.api_key` |
| `MINIMAX_API_KEY` | `minimax.api_key` |
| `MIMO_API_KEY` | `mimo.api_key` |
| `SUPABASE_API_KEY` | `supabase.api_key` |
| `PGVECTOR_CONNECTION_STRING` | `pgvector.connection_string` |

非密钥类设置（模型、音色、调优参数）仍然只放在 `config.toml` 中。

各服务商的具体行为与注意事项见[服务商](./providers.zh-CN.md)。
