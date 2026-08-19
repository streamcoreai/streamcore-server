[English](./providers.md) | **简体中文**

# 服务商集成

| 角色 | 服务商 | 所需凭据 |
|------|-----------|----------------------|
| STT | `aliyun`、`assemblyai`、`deepgram`、`openai`、`vibevoice`、`volcengine` | 对应服务商的 API key，或一个本地 VibeVoice ASR 服务 |
| LLM | `openai`、`ollama`、`agent` | OpenAI API key、你自己掌控的 Ollama 实例，或你自己的 HTTP 智能体端点 |
| TTS | `cartesia`、`deepgram`、`elevenlabs`、`mimo`、`minimax`、`speechify`、`vibevoice` | 对应服务商的 API key，或一个本地 VibeVoice TTS 服务 |
| 语音到语音 | `grok` | xAI API key —— 一并取代 STT、LLM 与 TTS |
| RAG（可选） | `pgvector`、`supabase` | Postgres 连接串或 Supabase URL + key，另需 OpenAI key 用于 embedding |

注意：

- `stt.provider = "openai"` 使用 Whisper 式的最终转写，而不是流式中间结果。
- `llm.provider = "ollama"` 通过 `base_url` 指向任何兼容 Ollama 的端点 —— 本地或你自己的基础设施均可。
- `llm.provider = "agent"` 把每一轮对话 POST 到你托管的 HTTP 端点；记忆、提示词与工具都由你的智能体掌控，回复以 SSE、分块文本或 JSON 流式返回。见[接入你自己的智能体](./bring-your-own-agent.zh-CN.md)。
- `stt.provider = "vibevoice"` 与 `tts.provider = "vibevoice"` 使用本地模型；请先启动 Python 边车进程。
- `tts.provider = "minimax"` 覆盖 40+ 语言，是中文场景下最强的选项。区域与套餐相关的坑见 [MiniMax TTS](#minimax-tts)。
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
