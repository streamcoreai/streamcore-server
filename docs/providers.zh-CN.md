[English](./providers.md) | **简体中文**

# 服务商集成

| 角色 | 服务商 | 所需凭据 |
|------|-----------|----------------------|
| STT | `aliyun`、`assemblyai`、`deepgram`、`openai`、`telnyx`、`vibevoice`、`volcengine` | 对应服务商的 API key，或一个本地 VibeVoice ASR 服务 |
| LLM | `openai`、`ollama`、`agent` | OpenAI API key、你自己掌控的 Ollama 实例，或你自己的 HTTP 智能体端点 |
| TTS | `cartesia`、`deepgram`、`elevenlabs`、`mimo`、`minimax`、`speechify`、`telnyx`、`vibevoice` | 对应服务商的 API key，或一个本地 VibeVoice TTS 服务 |
| 语音到语音 | `grok` | xAI API key —— 一并取代 STT、LLM 与 TTS |
| RAG（可选） | `pgvector`、`supabase` | Postgres 连接串或 Supabase URL + key，另需 OpenAI key 用于 embedding |

注意：

- `stt.provider = "openai"` 使用批量最终转写而不是流式中间结果，因此实时字幕只显示最终结果，打断降级为等满 600ms 回应窗口后仅凭 VAD 触发（没有文本，无法对短促语做分类）；可通过 `openai.stt_model` 选择 `whisper-1`、`gpt-4o-transcribe` 或 `gpt-4o-mini-transcribe`。
- `stt.provider = "telnyx"` 通过一条 WebSocket、一个 key 前置自研与十余种托管转写引擎。`transcription_engine` 默认 `Deepgram`，打断与实时字幕开箱即用；自研 `Telnyx` 引擎只出最终结果，实时字幕只显示最终结果，打断降级为等满回应窗口后仅凭 VAD 触发。见 [Telnyx STT](#telnyx-stt)。
- `llm.provider = "ollama"` 通过 `base_url` 指向任何兼容 Ollama 的端点 —— 本地或你自己的基础设施均可。
- `llm.provider = "agent"` 把每一轮对话 POST 到你托管的 HTTP 端点；记忆、提示词与工具都由你的智能体掌控，回复以 SSE、分块文本或 JSON 流式返回。见[接入你自己的智能体](./bring-your-own-agent.zh-CN.md)。
- `stt.provider = "vibevoice"` 与 `tts.provider = "vibevoice"` 使用本地模型；请先启动 Python 边车进程。
- `tts.provider = "minimax"` 覆盖 40+ 语言，是中文场景下最强的选项。区域与套餐相关的坑见 [MiniMax TTS](#minimax-tts)。
- `tts.provider = "telnyx"` 是 Telnyx 托管合成，每个话语一条 WebSocket 连接；音色可用性因账号而异。连接模型与音色目录见 [Telnyx TTS](#telnyx-tts)。
- `tts.provider = "mimo"` 是小米 MiMo TTS，中英文音色齐备，付费模型还支持声音克隆。
- `stt.provider = "aliyun"` 是阿里云百炼（DashScope）流式 ASR；`vocabulary_id` 可以把模型往你的领域词上带。
- `stt.provider = "volcengine"` 是豆包流式 ASR —— 适合 Deepgram 访问慢、或它的中文识别不够好的场景。控制台有免费时长可以先试。
- `realtime.provider = "grok"` 切换到语音到语音模式，并完全忽略 `[stt]`、`[llm]` 与 `[tts]`。

所有 key 与可调项都在[配置参考](./configuration.zh-CN.md)里。

## 语音到语音（Grok Voice）

设置 `realtime.provider` 会把三段式的 STT → LLM → TTS 链路换成单个模型：它接收用户音频，并直接以音频回答。转写、推理与合成在一跳内完成，从而消除了经典链路中主导每轮时延的那两次交接。

```toml
[realtime]
provider = "grok"

[grok]
api_key = "xai-..."
model = "grok-voice-think-fast-2.0"   # 或 "grok-voice-latest"
voice = "eve"
reasoning_effort = "high"             # "none" 用细腻度换时延
system_prompt = "You are a helpful assistant on a phone call. Keep it short."
```

流水线两端仍是同一条 Opus/RTP 链路，中间以单个 `runRealtime` 循环取代 `runInbound` + `runAgent`。音频以 16 kHz PCM 通过二进制 WebSocket 帧协商 —— 正是流水线的原生采样率，因此音频路径上既不重采样也不做 base64 编码。

### 模型

| `model` | 说明 | 价格 |
|---|---|---|
| `grok-voice-think-fast-2.0` | 最新、能力最强。默认开启推理 | $0.08 / 分钟（$4.80 / 小时） |
| `grok-voice-think-fast-1.0` | 上一代，更便宜 | $0.05 / 分钟（$3.00 / 小时） |
| `grok-voice-latest` | 始终指向最新模型的别名 —— 当前为 `grok-voice-think-fast-2.0` | 跟随其解析到的模型 |

两个模型的文本输入都另计 $0.004 每次。生产环境请固定带版本的名称：`grok-voice-latest` 会在 xAI 发布新模型时改变指向，从而在运行中的部署下悄悄改变行为与价格。

为这些模型写提示词时要注意两点：

- **`system_prompt` 要短。** 它们足够强，把为更弱模型写的长篇 GPT 时代提示词原样搬过来反而会变差。xAI 自己的建议是：删掉那些绕过缺陷的提示技巧与边缘情况补丁。
- **推理默认开启。** `reasoning_effort = "high"` 对多步指令、语气细腻度与含糊问题有帮助。当智能体的任务很简单时，设为 `"none"` 可降低时延。

模型并不知道自己被部署在什么产品里 —— 如果你希望它自报家门（「你是 StreamCore 助手」），那属于 `system_prompt` 的内容。

### 音色

`voice` 接受一个小写的内置音色 ID（默认 `eve`），或通过 xAI Custom Voices API 克隆的自定义音色 ID。用 `GET /v1/tts/voices` 获取当前可用列表。这些音色与 TTS API 共用，因此 xAI TTS 音色表里的任何一个在这里都可用。

### 该模式下有什么变化

| 能力 | 行为 |
|---|---|
| 轮次检测与插话打断 | 完全由模型的服务端 VAD 掌管，见下文 |
| 插件、技能、视觉、车控 | 注册为 function tool；两种模式下运行的是同一批 handler |
| RAG | 以 `knowledge_search` 工具的形式暴露，由模型按需调用，而不是注入到每次提示中 |
| 托管搜索 | `web_search` 与 `x_search` 在 xAI 侧运行，无需本地插件 |
| 滚动摘要、误解检测 | 不使用 —— 它们作用于模型根本不产出的 STT 转写 |
| 表达标签（`[warm]`、`[calm]`） | 不使用 —— 韵律由模型自己控制 |
| 插件 `thinking_sound` | 不播放 —— 它会与仍在出向队列中排空的模型音频交叠。每通电话记录一次日志 |

### 插话打断与轮次检测

检测打断的是模型，而不是服务端。Grok 的 VAD 判定用户插话后会停止生成，并发送 `input_audio_buffer.speech_started`；服务端唯一要做的是丢弃本地已经缓冲的音频，因为模型无法收回已经排在这里的帧。

这意味着**在 realtime 模式下 `[pipeline] barge_in` 不起作用**。它只被 `runInbound` 读取，而后者根本不运行。本地能量 VAD、回应词抑制窗口、`readback_bargein_guard_enabled` 与音量压低同理 —— 这里的打断是硬切断，而不是先压低再恢复。仍然建议保留 `barge_in = true`，这样切回经典链路时设置依然正确。

调优转移到 `[grok]`：

| 设置 | 什么时候用 |
|---|---|
| `vad_threshold`（0.1–0.9，默认 0.85） | 噪音、咳嗽或「嗯嗯」把智能体打断了，就调高它。这是软件层回应词抑制在此模式下最接近的替代品 |
| `silence_duration_ms` | 用户说到一半被切断，就调高它以容纳更长的停顿 |
| `prefix_padding_ms`（默认 333） | 一轮开头的第一个词被吃掉了，就调高它 |
| `idle_timeout_ms` | 你希望智能体在静默后主动接话。不设置则关闭该检查 |

没有办法在保留自动轮次控制的同时关闭打断：`turn_detection` 只能是 `server_vad` 或 `null`，而 `null` 意味着服务端必须自己决定每一轮何时结束并显式请求每次回复。请改为调阈值。

### 转写

`transcription = true` 会单独跑一遍转写，纯粹是为了让客户端收到用于展示的 `transcript` 事件 —— 模型本身直接听音频，并不需要它。如果你的客户端不展示转写，关掉它可以省下这部分费用。

这些转写是累积式的，并且分片到达：一次更新可能会修订它先前已经发出的词，而一个说到一半停顿的用户会为同一个问题产生多个 finalised 分片。服务端会把它们合并为一轮，并在模型开始回复时提交，因此一次口头发言只渲染成一条消息。设置 `[pipeline] debug = true` 可以把每个服务商事件连同其转写载荷一起记录下来。

### 成本

计费按音频的墙钟分钟数而不是 token 计算，这改变了它与自行拼装链路之间的经济账 —— 通话空闲时间照样计费，因此 `idle_timeout_ms` 与及时挂断在这里比经典模式更重要。费率见上面的模型表。

## MiniMax TTS

MiniMax 的 T2A v2 API，走 SSE，覆盖 40+ 语言，中文音色阵容很强。它是唯一一个能按你要求的采样率输出 PCM 的托管服务商，因此服务端直接请求 16 kHz 单声道 —— 流水线的原生采样率 —— 送进编码器之前无需任何重采样。

```toml
[tts]
provider = "minimax"

[minimax]
api_key = ""
voice_id = "English_Graceful_Lady"   # 或 "Chinese (Mandarin)_News_Anchor"
model = "speech-2.6-turbo"
# base_url = "https://api.minimax.io/v1"
```

有三件事必须弄对：

- **区域。** `base_url` 默认指向全球端点 `https://api.minimax.io/v1`。在中国大陆平台注册的账号必须改为 `https://api.minimaxi.com/v1` —— 两个平台的 key 不通用，用错主机会直接鉴权失败，而不会自动回退。
- **模型与套餐。** `speech-2.6-turbo` 是低时延档位，也是实时音频链路上正确的默认值；`-hd` 系列音质更好，但会增加数百毫秒。Token Plan 的 key（`sk-cp-`）只覆盖 `speech-2.8-hd` —— 其他任何模型都会走按量计费，余额为零时报错 `2056`。
- **错误以 HTTP 200 返回。** MiniMax 把鉴权与配额失败放在 200 响应体的 `base_resp.status_code` 字段里。客户端会检查它，因此这些问题会以真实错误的形式暴露，而不是变成静默的空音频。

表达标签映射到 MiniMax 的情绪枚举：`[warm]` 与 `[excited]` 变成 `happy`，`[calm]` 与 `[empathetic]` 变成 `calm`。`[empathetic]` 刻意落在 `calm` 而不是 `sad` —— 后者在道歉与坏消息场景下会过头，听起来像在难过。没有情绪映射的标签仍会通过语速生效，语速被限制在 MiniMax 的 0.5–2.0 范围内。

## Telnyx TTS

Telnyx 托管语音合成，走 WebSocket，以流水线原生的 16 kHz linear16 流式输出，音频路径无需任何重采样。

```toml
[tts]
provider = "telnyx"

[telnyx]
api_key = ""
voice = "Telnyx.Qwen3TTS.d9348e0d-988a-42cc-a64e-18093fe45c03"
voice_speed = 1.0
```

有三件事必须弄对：

- **每个话语一条 WebSocket 连接。** 协议没有逐话语的完成标记：只有客户端发出空文本 teardown 后，服务端才会发 `isFinal`，因此客户端为每个话语新建一条连接：init、文本、teardown、收齐音频直到 final 帧、服务端关闭。与常驻连接相比，这在实时链路上没有代价：合成速度约为播放的 2.4 倍，拨号后不到一秒就有首个音频到达。
- **音色因账号而异。** `voice` 是 `GET /v2/text-to-speech/voices` 目录中的任意名称，可用性因账号而异：你的 key 未开通的音色会在 WebSocket 握手阶段就返回 HTTP 403，而不是在通话中途报错。配置默认值（`Telnyx.Qwen3TTS.d9348e0d-988a-42cc-a64e-18093fe45c03`）是一个已验证的 Qwen3TTS 音色（目录中名为 Delta，女声），并不对每个 key 都保证可用。
- **Telnyx 的 LLM 无需新增服务商。** `openai.base_url = "https://api.telnyx.com/v2/ai"` 即可让现有的 `openai` LLM 服务商直连 Telnyx 推理（例如模型 `glm-5.3`），零代码改动。

表达标签映射到 `voice_speed`（限制在 0.8–1.2，与 Cartesia 相同的对话档位），配置里的 `voice_speed` 则是未打标签句子的基准语速。

## Telnyx STT

走 Telnyx 语音转文字 WebSocket 的流式识别：上行是裸 linear16 二进制帧，下行是 JSON 转写帧，均为流水线原生的 16 kHz 单声道，音频路径无需任何重采样。与 TTS 共用同一个 `[telnyx]` 配置段和 API key；`transcription_engine` 选择识别引擎。

```toml
[stt]
provider = "telnyx"

[telnyx]
api_key = ""
transcription_engine = "Deepgram"   # 已验证取值："Deepgram"（有中间结果，打断可用）或 "Telnyx"（自研，只出最终结果）。大小写敏感
```

有三件事必须弄对：

- **默认是 `Deepgram`。** 它像内置的 Deepgram 服务商一样流式输出中间结果，打断（barge-in）与实时字幕开箱即用。一个只出最终结果的默认引擎，会让任何只改了 provider 就开始说话的人悄无声息地让这两项能力降级：打断退化为仅凭 VAD，实时字幕只剩最终结果。
- **自研 `Telnyx` 引擎只出最终结果。** 它在来电者停止说话后才发出唯一一帧 final —— 没有中间结果、没有时间戳、置信度为 `null`。客户端字幕要等到 final 落地才有内容。打断没有中间文本可判定，因此是降级而不是关闭：来电者压过智能体说话时，抑制窗口仅凭 VAD 打开，只有说话持续超过整个 600ms 回应窗口后才确认打断；窗口内结束的短促语一律按回应词处理 —— 没有文本就无法把它和 "嗯嗯" 区分开。持续抢话可以打断，短促抢话不能。因为该引擎只在整个话语结束后才应答，客户端自己做端点检测（`internal/vad`，即流水线在用的同一个检测器，静音窗口与 `openai.go` 相同），每个话语开一条新连接，final 落地后由客户端关闭（服务端会一直握着连接不放）。启动时日志里会写明本会话的打断以仅凭 VAD 的降级模式运行，运维从日志就能知道。取舍讨论见[设计讨论](https://github.com/streamcoreai/streamcore-server/issues/75)。
- **引擎名大小写敏感，原样透传。** `telnyx` 会被一帧结构化错误拒绝，错误里列出支持的引擎。此处只验证了 `Deepgram` 与 `Telnyx` 两个取值；该端点前置的其他托管引擎（AssemblyAI、Azure 及列表中的其余引擎）可透传但未经测试。

置信度：自研引擎返回 `null`，托管引擎返回 0-1 浮点数；流水线把 `null` 视为未知而不是低置信。

## 本地 VibeVoice 配置

VibeVoice 提供完全本地、无需 API key 的 STT 与 TTS：识别用 [VibeVoice-ASR](https://huggingface.co/mlx-community/VibeVoice-ASR-4bit)，合成用 [VibeVoice-Realtime-0.5B](https://huggingface.co/mlx-community/VibeVoice-Realtime-0.5B-6bit)，通过两个轻量 Python 边车进程运行。在 Apple Silicon 上使用 [mlx-audio](https://github.com/Blaizzy/mlx-audio)（MLX）；在 Linux/Windows 上自动回退到 PyTorch。

```bash
# Apple Silicon (MLX)
pip install mlx-audio numpy websockets fastapi uvicorn
# 或 PyTorch（Linux / CUDA）
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

ASR 服务通过 WebSocket 接收实时 PCM 并输出 JSON 转写事件。TTS 服务接收 HTTP POST 并返回裸 PCM。
