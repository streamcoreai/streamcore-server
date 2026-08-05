# StreamCore
[![Discord](https://img.shields.io/badge/join-us%20on%20discord-5865F2?logo=discord&logoColor=white)](https://discord.gg/xKGFaGWawT)
[![Follow @jasonshen_](https://img.shields.io/badge/follow-%40jasonshen__-000000?logo=x&logoColor=white)](https://x.com/jasonshen_)

[English](./README.md) | **简体中文**

**面向 AI 应用的实时媒体基础设施。**

StreamCore 负责用户、设备、通信网络与 AI 服务之间那条对延迟极其敏感的媒体链路。

它提供 WebRTC 音频传输、会话管理、流式语音集成、打断处理、实时事件以及客户端 SDK —— 而智能体、模型、工具和业务逻辑始终由你的应用掌控。

> **StreamCore 是连接你的用户与你的智能的实时媒体层。**
>
> 智能体自己带，实时媒体交给 StreamCore。

用 StreamCore 可以构建：

- 语音智能体
- 实时副驾（copilot）
- 实时翻译
- AI 主持的音频体验
- 嵌入式语音设备
- 电话与通信类应用
- 各类定制化实时 AI 产品

本仓库是 Go 媒体运行时 —— StreamCore 项目家族中的核心服务端组件。

## StreamCore 解决的问题

做一个智能体 demo 很容易，围绕它搭建可靠的实时媒体基础设施则不然。

当原型要变成产品时，难点从来不是提示词：

| 问题 | StreamCore 今天做了什么 |
|---------|----------------------------|
| WebRTC 连通性 | 基于 Pion 的 peer、完整 ICE 收集、单次 HTTP POST 完成 WHIP 信令 |
| NAT 穿透 | 内置 STUN/TURN 服务器 —— 无需额外的 coturn 容器 |
| 音频传输 | 双向 Opus over RTP，编解码已为你处理好 |
| 轮次切换 | 自适应 VAD 会跟踪每通呼叫的噪声底，并通过去抖把说话人句中的停顿合并为一个轮次 |
| 打断 | 采用更快的 VAD 档位做插话检测：智能体音频先行压低，回应词（"嗯"、"好的"）被过滤掉，确认的打断会取消进行中的 LLM 与 TTS |
| 流式服务商集成 | 流式 STT、流式 LLM、分块流式 TTS，音频在合成完成前就开始播放 —— 端到端已打通 |
| 会话状态 | 服务端生成的 session ID、多 peer 会话、生命周期与拆除 |
| 实时事件 | 通过 DataChannel 推送 transcript、response、agent state 和延迟耗时事件 |
| 客户端集成 | TypeScript、React Native、Python、Go、Rust SDK |
| 电话 | SIP 桥接组件，PCMU ↔ Opus 转码并通过 WHIP 接入 |
| 鉴权 | `/whip` 上可选的 JWT 鉴权，以及短时效 token 签发端点 |
| 延迟可观测性 | DataChannel 耗时事件，外加日志中按轮次的延迟拆解（端点检测、合并、embedding、向量检索、LLM、TTS） |

表格之外的东西 —— 水平扩展、会话重连、metrics 端点 —— 都在 [路线图](#路线图) 里，还没进产品。

## 它的位置

```text
┌───────────────────────────────────────────────┐
│              Applications                     │
│ Voice agents · Copilots · Translation · Rooms │
└───────────────────────┬───────────────────────┘
                        │ SDKs and realtime events
┌───────────────────────▼───────────────────────┐
│           StreamCore Media Runtime            │
│                                               │
│ WebRTC · RTP · Opus · Sessions · Interruption │
│ VAD · Streaming audio · Network traversal     │
└───────────────┬───────────────────┬───────────┘
                │                   │
       ┌────────▼────────┐  ┌───────▼──────────┐
       │ AI and speech   │  │ Application and  │
       │ services        │  │ agent backends   │
       │ STT · TTS · LLM │  │ Tools · APIs     │
       └─────────────────┘  └──────────────────┘
```

StreamCore 可以跑完整的「语音 → 智能体 → 语音」流水线，但那只是用法之一。

## 不只是一个智能体框架

大多数智能体框架从提示词、工具和模型编排开始。StreamCore 从更底一层开始：实时媒体链路 —— 传输、编解码、语音流、轮次切换、打断、网络穿透、会话状态，以及与 AI 服务的通信。

你的智能体不必住在 StreamCore 里面。有四种受支持的方式让你完全掌控智能：

**1. 把智能体藏在一次工具调用后面。** 插件（Python、TypeScript、JavaScript，走 JSON-RPC）和原生 Go 工具让对话可以调用你已有的后端 —— 你的 API、你的编排、你的数据。StreamCore 负责把结果以语音流回去。

**2. 把模型层指向你自己的基础设施。** `llm.provider = "ollama"` 配合自定义 `base_url`，可以指向任何你自己运行的 Ollama 兼容端点，包括挡在你自己的路由或模型栈前面的那一个。

**3. 直接实现 LLM 接口。** 模型层就是 [`internal/llm/llm.go`](./internal/llm/llm.go) 里一个很小的 Go 接口：

```go
type Client interface {
    Chat(ctx context.Context, userText string, onChunk func(string), onSentence func(string)) (string, error)
    // OneShot is a single non-streaming call, independent of conversation
    // state. Used for background work such as the rolling summary.
    OneShot(ctx context.Context, system, user string) (string, error)
    SetTools(tools []ToolDefinition)
    SetToolHandler(handler func(ctx context.Context, call ToolCall) (string, error))
    AppendSystemPrompt(text string)
    Reset()
}
```

针对你的智能体服务实现它，在 `NewClient` 中注册，整条媒体链路 —— 传输、VAD、插话打断、TTS 分块、事件 —— 原封不动照常工作。

**4. 使用 StreamCore 可选的内置智能体运行时。** LLM 编排、工具、技能、RAG 和对话历史都开箱即用，你想用就用。见 [可选的智能体运行时](#可选的智能体运行时)。

一个无需写 Go 代码的通用 HTTP / OpenAI 兼容智能体端点在[路线图](#路线图)上；今天的方案 3 是一个小文件，不是一次 fork。

## 实时媒体能力

**传输与连通性**

- 基于 WebRTC 的双向 Opus 音频（`sendrecv`）
- WHIP 信令（[RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html)）—— 一次 HTTP `POST` 完成 SDP 交换，无需常驻信令连接
- 双端完整 ICE 收集，不使用 trickle ICE
- 基于 Pion 的内置 STUN/TURN 服务器（UDP **和 TCP** 3478，中继端口 50001–60000）—— 支持 TCP，所以在封禁 UDP 的防火墙后面依然能接通
- `/whip` 上可选的 JWT 鉴权，`POST /token` 签发 1 小时有效期的 token
- `/health` 端点，以及带强制退出兜底的优雅关闭

**媒体链路**

- Opus 解码 → PCM → 流水线 → PCM → Opus 编码 → RTP
- 基于能量的 VAD，起始/结束帧数可配置，并会自适应每通呼叫的噪声底 —— 干净线路上的轻声说话者和路边的说话者都能被识别
- 更快 VAD 档位下的插话打断：说话人抢话时智能体音频压低，若判定只是回应词则恢复
- 轮次去抖，把连续的 final 转写合并，让「我想…嗯…订个位」只被回答一次而不是两次
- 按句边界分块，让 TTS 在 LLM 生成完成前就开始；再加上 chunk 级流式，让一句话还没合成完音频就已经在播
- 可选的逐句表达标签 —— 模型可以在句首加 `[warm]`、`[empathetic]`、`[calm]` 或 `[excited]`，它们会映射到服务商的语音控制参数，并且绝不会被念出来
- 思考音 —— 慢工具运行时通过 RTP 流播放的可选提示音（500 ms 宽限期）

**会话与事件**

- 服务端生成的 session ID，内存态会话管理器
- 一个会话可以有多个 peer，每个 peer 有入站或出站方向
- DataChannel `events` 通道推送 `transcript`、`response`、`state` 和 `timing`
- 当 `pipeline.debug = true` 时记录按轮次的延迟拆解，分别区分端点检测、轮次合并、embedding、向量检索、LLM 和 TTS
- 入站 DataChannel 消息会被路由进流水线（今天用于摄像头图像分片）

**客户端**

- TypeScript（`@streamcore/js-sdk`）、Python（`streamcore`）、Go（`github.com/streamcoreai/go-sdk`）、Rust
- React Native / Expo（`@streamcore/react-native-sdk`）—— 已构建，尚未发布到 npm

## 支持的端点与集成

| 端点类型 | 状态 | 方式 |
|---------------|--------|-----|
| 浏览器 | 可用 | TypeScript SDK over WHIP |
| 移动端 | 可用 | React Native / Expo SDK（peer 依赖 `react-native-webrtc`） |
| 后端服务 / worker | 可用 | Go、Python 或 Rust SDK |
| CLI 与 TUI | 可用 | Go 和 Rust 示例 |
| 电话（SIP） | 可用 | [`sip-server`](https://github.com/streamcoreai/sip-server) 桥接 PCMU/RTP ↔ Opus/WHIP，支持呼入与呼出 |
| 嵌入式设备 | 实验性 | [`esp32`](https://github.com/streamcoreai/esp32) 中直接讲 WHIP 的 ESP32-S3 固件 |

| AI 集成 | 服务商 |
|----------------|-----------|
| 流式 STT | Deepgram、AssemblyAI、OpenAI、VibeVoice（本地） |
| LLM | OpenAI、Ollama（本地或自托管） |
| 流式 TTS | Cartesia、Deepgram、ElevenLabs、Speechify、VibeVoice（本地） |
| 语音到语音 | xAI Grok Voice（一个模型取代 STT + LLM + TTS） |
| 检索 | pgvector、Supabase |
| 自定义工具 | Python / TypeScript / JavaScript 插件，原生 Go 工具 |

## 演示

<a href="https://www.loom.com/share/ee079aca75aa4fa1ba6a5e51302fbd56" target="_blank">
  <img src="https://cdn.loom.com/sessions/thumbnails/ee079aca75aa4fa1ba6a5e51302fbd56-e4ee3f1f1a14a51d.jpg" alt="Demo Video" />
</a>

## 赞助与支持

<!-- The public live demo is powered by generous API credits from our sponsors. -->

<div align="center">
<!-- Logos will go here once received -->
</div>

感谢！有兴趣赞助？欢迎联系，可在 GitHub 与 demo 页面展示 logo。

## 快速开始

### 前置条件

使用 Docker：Docker 和 Docker Compose。

本地开发：

- Go 1.22+
- Node.js 20+ 和 npm
- Python 3.10+（用于 Python 插件或示例）
- Rust 1.87+（用于 Rust SDK 或示例）

### 运行媒体运行时

```bash
cp config.toml.example config.toml
# Edit config.toml with your provider credentials

go run .
```

或者用 Docker：

```bash
docker build -t streamcore-server .
docker run --rm -p 8080:8080 -v "$(pwd)/config.toml:/config.toml:ro" streamcore-server
```

服务监听 `:8080`。客户端连接 `http://localhost:8080/whip`。

### 接入一个客户端

```bash
git clone https://github.com/streamcoreai/examples.git
cd examples/typescript
npm install
npm run dev
```

打开 [http://localhost:3000](http://localhost:3000)。它默认连接 `http://localhost:8080/whip`。

### 接入你自己的后端

StreamCore 的要点在于智能是你的。最快的路径是一个调用你服务的工具 —— 你的后端在干活时，智能体还能继续说话。

```bash
mkdir -p plugins/plugins/orders-lookup
```

`plugins/plugins/orders-lookup/plugin.yaml`

```yaml
name: orders.lookup
description: Look up an order by ID in the company order system
version: 1
language: python
entrypoint: main.py
thinking_sound: true
parameters:
  type: object
  properties:
    order_id:
      type: string
      description: The customer's order ID
  required:
    - order_id
```

`plugins/plugins/orders-lookup/main.py`

```python
import os, requests
from streamcoreai_plugin import StreamCoreAIPlugin

plugin = StreamCoreAIPlugin()

@plugin.on_execute
def handle(params):
    r = requests.get(
        f"{os.environ['BACKEND_URL']}/orders/{params['order_id']}",
        timeout=10,
    )
    r.raise_for_status()
    order = r.json()
    return f"Order {order['id']} is {order['status']}, arriving {order['eta']}."

plugin.run()
```

重启服务。你的后端现在已经是实时语音会话的一部分，而围绕它的媒体链路上每一毫秒都由 StreamCore 处理。

如果你想掌控整段对话而不只是一次工具调用，请实现 [不只是一个智能体框架](#不只是一个智能体框架) 中描述的 `llm.Client` 接口。

### 完全本地运行（无需 API key）

用 Ollama 跑 LLM、VibeVoice 跑 STT/TTS，一切都在你自己的硬件上。

**1. 安装并启动 Ollama**

```bash
brew install ollama            # macOS; see https://ollama.ai for Linux
ollama serve
ollama pull gpt-oss:20b
```

**2. 启动 VibeVoice 边车进程**

```bash
# Apple Silicon (MLX)
pip install mlx-audio numpy websockets fastapi uvicorn
# OR Linux / CUDA
# pip install torch transformers librosa numpy websockets fastapi uvicorn

python external/vibeVoice/vibeVoiceAsr/server.py   # ws://127.0.0.1:8200
python external/vibeVoice/vibeVoiceTTS/server.py   # http://127.0.0.1:8300
```

**3. 配置并运行**

```toml
[stt]
provider = "vibevoice"

[llm]
provider = "ollama"

[tts]
provider = "vibevoice"

[ollama]
base_url = "http://localhost:11434"
model = "gpt-oss:20b"

[vibevoice]
asr_url = "ws://127.0.0.1:8200"
tts_url = "http://127.0.0.1:8300"
voice = "en-Emma_woman"
```

```bash
go run .
```

完全本地的实时语音，不依赖任何外部 API。细节见 [本地 VibeVoice 配置](#本地-vibevoice-配置)。

## 可选的智能体运行时

以下内容全部是可选的。如果你的智能体活在自己的技术栈里，可以整节跳过。

当你确实想让 StreamCore 来跑对话时，它提供带对话历史的 LLM 编排、工具、行为技能，以及内联检索。

一旦启用内置运行时，有两个行为会自动生效：

- **滚动摘要。** 长通话会超出模型的历史窗口。较早的轮次会在后台被摘要并作为上下文注入，于是第一分钟出现的事实能活到第十分钟。
- **低置信度处理。** 当语音识别报告置信度较差时，智能体会被告知请对方重复一遍而不是猜测；若连续多轮如此则会升级处理。

### 插件与技能

插件赋予智能体**能力**。技能塑造它的**行为**。

- 插件调用 API、数据库、日历、CRM、工作流和内部工具
- 技能定义语气、人格、护栏、品牌调性和流程指引

插件以 Python、TypeScript 或 JavaScript 进程的形式通过 JSON-RPC 运行。技能是注入到系统提示词中的 Markdown 文件。示例插件和技能位于 [`plugins/`](./plugins/)。若想零 IPC 开销地扩展，可用 `pluginMgr.RegisterNative(...)` 注册原生 Go 工具。

**插件清单（manifest）参考**

| 字段 | 类型 | 必填 | 说明 |
|-------|------|----------|-------------|
| `name` | string | 是 | LLM 调用的唯一工具名（如 `weather.get`） |
| `description` | string | 是 | 工具做什么 —— 会展示给 LLM |
| `version` | int | 是 | 清单版本 |
| `language` | string | 是 | `python`、`typescript` 或 `javascript` |
| `entrypoint` | string | 是 | 要运行的文件（如 `main.py`、`index.ts`） |
| `parameters` | object | 是 | 描述工具参数的 JSON Schema |
| `confirmation_required` | bool | 否 | 执行前智能体先向用户确认（默认 `false`） |
| `thinking_sound` | bool | 否 | 工具运行时在 500 ms 宽限期后播放轻柔循环提示音（默认 `false`） |

**内置插件**

| 插件 | 语言 | 说明 |
|--------|----------|-------------|
| `math.calculate` | TypeScript | 计算数学表达式 |
| `weather.get` | TypeScript | 查询某地当前天气 |
| `time.get` | Python | 任意时区的当前日期/时间 |
| `vision.analyze` | TypeScript | 分析来自设备摄像头的图像 |
| `gmail` | TypeScript | 通过 Gmail 读取和发送邮件（OAuth2）—— 见 [Gmail 插件 README](plugins/plugins/gmail/README.md) |

**内置技能**

| 技能 | 说明 |
|-------|-------------|
| `tool-savvy` | 引导智能体使用工具而不是靠猜 |
| `friendly-conversationalist` | 温暖自然的对话人格 |
| `polite-assistant` | 简洁礼貌的语音交互风格 |
| `concise-responder` | 保持回答简短，适合口语播报 |
| `error-recovery` | 在语音对话中优雅地处理错误 |
| `vision-assistant` | 启用基于摄像头的图像分析 |
| `gmail-assistant` | 逐封处理邮件，带回复与确认流程 |

插件 SDK：`@streamcore/plugin`（TypeScript）、`streamcore-plugin`（Python）。

### 检索（RAG）

RAG 内联运行在媒体流水线中：服务端对用户这一轮做 embedding，从你的向量库取回 top-k 片段，并在 LLM 调用前注入 —— 只有一次 LLM 调用，没有工具调用的往返。

有两件事让检索不落在关键路径上。没有实义词的轮次（"好的、行、谢谢"）会被跳过，因为没有可用于向量检索的锚点。另外，设置 `pipeline.rag_prefetch = true` 后，检索会在轮次合并窗口内投机式启动，于是 embedding 与向量检索的往返与流水线本来就要等的那段时间重叠，而不是叠加。

| 服务商 | 后端 | 配置段 |
|----------|---------|----------------|
| `pgvector` | 带 pgvector 扩展的 PostgreSQL | `[pgvector]` |
| `supabase` | Supabase（HTTP 上的 Postgres RPC） | `[supabase]` |

两者都使用 OpenAI embedding（默认 `text-embedding-3-small`），所以必须设置 `[openai].api_key`。省略 `[rag]` 段即可完全关闭检索。

**pgvector 配置**

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE documents (
    id SERIAL PRIMARY KEY,
    content TEXT NOT NULL,
    embedding vector(1536),
    source TEXT
);
```

```toml
[rag]
provider = "pgvector"

[pgvector]
connection_string = "postgres://user:pass@localhost:5432/mydb"
```

**Supabase 配置**

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE documents (
    id SERIAL PRIMARY KEY,
    content TEXT NOT NULL,
    embedding vector(1536),
    source TEXT,
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE OR REPLACE FUNCTION match_documents(
    query_embedding vector(1536),
    match_count int DEFAULT 3
)
RETURNS TABLE (content text, similarity float)
LANGUAGE plpgsql AS $$
BEGIN
    RETURN QUERY
    SELECT d.content, 1 - (d.embedding <=> query_embedding) AS similarity
    FROM documents d
    ORDER BY d.embedding <=> query_embedding
    LIMIT match_count;
END;
$$;

ALTER TABLE documents ENABLE ROW LEVEL SECURITY;

CREATE POLICY "Allow read access to documents"
ON documents FOR SELECT TO authenticated, anon USING (true);

CREATE POLICY "Allow insert access to documents"
ON documents FOR INSERT TO authenticated, anon WITH CHECK (true);

CREATE POLICY "Allow update access to documents"
ON documents FOR UPDATE TO authenticated, anon USING (true);
```

```toml
[rag]
provider = "supabase"

[supabase]
url = "https://xxx.supabase.co"
api_key = "your-service-role-key"
function = "match_documents"
table = "documents"
```

**导入文档**

服务端只负责查询期的检索。用 [`streamcore-cli`](https://github.com/streamcoreai/streamcore-cli) 往向量库里灌数据：

```bash
git clone https://github.com/streamcoreai/streamcore-cli
cd streamcore-cli && go build -o streamcore-cli .

# Supports .txt, .md, .csv, .pdf, .docx, .xlsx
streamcore-cli ingest docs/faq.pdf product-catalog.xlsx notes.md
streamcore-cli ingest --provider supabase --config ../server/config.toml data.csv
streamcore-cli ingest --chunk-size 256 --chunk-overlap 32 manual.docx
```

CLI 会读取服务端的 `config.toml` 获取服务商凭据，所以不用配置两遍。

| 参数 | 默认值 | 说明 |
|------|---------|-------------|
| `--config` | 自动探测 | 服务端 `config.toml` 的路径 |
| `--provider` | 取自配置 | 覆盖 RAG 服务商（`pgvector`、`supabase`） |
| `--chunk-size` | 512 | 目标分块大小（按词计） |
| `--chunk-overlap` | 64 | 分块之间的重叠（按词计） |

## 服务商集成

| 角色 | 服务商 | 所需凭据 |
|------|-----------|----------------------|
| STT | `deepgram`、`assemblyai`、`openai`、`vibevoice` | Deepgram API key、AssemblyAI API key、OpenAI API key，或本地 VibeVoice ASR 服务 |
| LLM | `openai`、`ollama` | OpenAI API key，或你自己掌控的 Ollama 实例 |
| TTS | `cartesia`、`deepgram`、`elevenlabs`、`speechify`、`vibevoice` | 对应服务商的 API key，或本地 VibeVoice TTS 服务 |
| 语音到语音 | `grok` | xAI API key —— 一并取代 STT、LLM 和 TTS |
| RAG（可选） | `pgvector`、`supabase` | Postgres 连接串或 Supabase URL + key，外加用于 embedding 的 OpenAI key |

注意：

- `stt.provider = "openai"` 使用 Whisper 风格的最终转写，而不是流式中间结果。
- `llm.provider = "ollama"` 通过 `base_url` 指向任意 Ollama 兼容端点 —— 本地或你自己的基础设施上。
- `stt.provider = "vibevoice"` 和 `tts.provider = "vibevoice"` 使用本地模型；请先启动 Python 边车进程。
- `realtime.provider = "grok"` 切换到语音到语音模式，并完全忽略 `[stt]`、`[llm]` 和 `[tts]`。

### 语音到语音（Grok Voice）

设置 `realtime.provider` 会把 STT → LLM → TTS 三段式链路换成单个模型：它接收说话人音频并用音频回答。转写、推理与合成一跳完成，从而去掉了经典流水线中主导轮次延迟的两次交接。

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

流水线两端仍保持相同的 Opus/RTP 链路，中间用单个 `runRealtime` 循环取代 `runInbound` + `runAgent`。音频以 16 kHz PCM 通过二进制 WebSocket 帧协商 —— 正是流水线的原生采样率，因此音频路径上不需要重采样，也不做 base64 编码。

#### 模型

| `model` | 说明 | 价格 |
|---|---|---|
| `grok-voice-think-fast-2.0` | 最新且能力最强。默认开启推理 | $0.08 / 分钟（$4.80 / 小时） |
| `grok-voice-think-fast-1.0` | 上一代，更便宜 | $0.05 / 分钟（$3.00 / 小时） |
| `grok-voice-latest` | 始终指向最新模型的别名 —— 当前为 `grok-voice-think-fast-2.0` | 跟随它解析到的模型 |

两个模型的文本输入还额外按 $0.004 计费。生产环境请固定带版本号的模型名：`grok-voice-latest` 会在 xAI 发布新模型时重新指向，导致运行中的部署行为和价格都变了。

给这类模型写提示词时要知道两件事：

- **`system_prompt` 要短。** 它们足够强，把 GPT 时代的长提示词原样搬过来反而更差。xAI 自己的建议是把为弱模型写的绕行提示和边界情况补丁都删掉。
- **推理默认开启。** `reasoning_effort = "high"` 有助于多步指令、细腻语气和模糊问题。当智能体任务简单时设为 `"none"` 以降低延迟。

模型并不知道自己被部署在什么产品里 —— 如果你想让它自报家门（"你是 StreamCore 助手"），那属于 `system_prompt` 的内容。

#### 音色

`voice` 接受小写的内置音色 ID（默认 `eve`），或通过 xAI 自定义音色 API 克隆出来的自定义音色 ID。用 `GET /v1/tts/voices` 获取当前音色列表。这些音色与 TTS API 共用，因此 xAI TTS 音色表里的任何一个都能用。

#### 该模式下有什么变化

| 能力 | 行为 |
|---|---|
| 轮次检测与插话打断 | 完全由模型的服务端 VAD 负责。见下文 |
| 插件、技能、视觉、车辆控制 | 注册为 function tool；两种模式下运行的是同一套处理函数 |
| RAG | 暴露为一个 `knowledge_search` 工具由模型按需调用，而不是注入到每次提示词中 |
| 托管搜索 | `web_search` 和 `x_search` 在 xAI 侧运行，不需要本地插件 |
| 滚动摘要、误听检测 | 不使用 —— 它们作用于模型根本不产出的 STT 转写 |
| 表达标签（`[warm]`、`[calm]`） | 不使用 —— 模型自己控制韵律 |
| 插件 `thinking_sound` | 不播放 —— 它会与仍在出站队列中排空的模型音频交错。每通呼叫记录一次日志 |

#### 插话打断与轮次检测

检测打断的是模型，不是服务端。Grok 的 VAD 判定说话人插话后会停止生成，并发送 `input_audio_buffer.speech_started`；服务端唯一要做的是丢弃本地已缓冲的音频，因为模型无法把已经排在这边队列里的帧收回去。

这意味着 **`[pipeline] barge_in` 在 realtime 模式下不起作用。** 它只被 `runInbound` 读取，而 `runInbound` 并不运行。同理，本地能量 VAD、回应词抑制窗口、`readback_bargein_guard_enabled` 和音频压低也都不生效 —— 这里的打断是硬切，而不是压低再恢复。仍然建议保持 `barge_in = true`，这样切回经典流水线时配置就是对的。

调优参数转移到了 `[grok]`：

| 设置 | 何时使用 |
|---|---|
| `vad_threshold`（0.1–0.9，默认 0.85） | 噪声、咳嗽或"嗯"会打断智能体。调高它。这是经典模式下软件回应词抑制最接近的替代 |
| `silence_duration_ms` | 说话人被句中截断。调高它以允许更长的停顿 |
| `prefix_padding_ms`（默认 333） | 一轮的第一个词被切掉。调高它 |
| `idle_timeout_ms` | 你希望智能体在沉默后主动搭话。不设置则关闭该检查 |

没有办法既保留自动轮次切换又禁用打断：`turn_detection` 只能是 `server_vad` 或 `null`，而 `null` 意味着服务端必须自己决定每一轮何时结束并显式请求每次回复。请改为调优阈值。

#### 转写

`transcription = true` 会单独跑一遍转写，纯粹是为了让客户端收到 `transcript` 事件用于展示 —— 模型本身直接听音频，并不需要它。如果你的客户端不显示转写，关掉它可以省下这笔开销。

这些转写是累积式的，并且分片到达：一次更新可能修订它已经发出的词，而一个句中停顿的说话人会为同一个问题产生多个 finalised 分片。服务端会把它们合并成一个轮次，并在模型开始回复时提交，因此一次口头发言渲染为一条消息。设置 `[pipeline] debug = true` 可记录每个服务商事件及其转写载荷。

#### 成本

计费按音频的墙钟分钟数而非 token，这改变了它相对于自行拼装流水线的经济性 —— 开着的通话即使空闲也在计费，所以 `idle_timeout_ms` 和及时拆除通话在这里比经典模式更重要。费率见上面的模型表。

### 本地 VibeVoice 配置

VibeVoice 提供完全本地、无需 API key 的 STT 和 TTS，识别使用 [VibeVoice-ASR](https://huggingface.co/mlx-community/VibeVoice-ASR-4bit)，合成使用 [VibeVoice-Realtime-0.5B](https://huggingface.co/mlx-community/VibeVoice-Realtime-0.5B-6bit)，通过两个轻量 Python 边车进程运行。在 Apple Silicon 上它们使用 [mlx-audio](https://github.com/Blaizzy/mlx-audio)（MLX）；在 Linux/Windows 上会自动回退到 PyTorch。

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

ASR 服务通过 WebSocket 接收实时 PCM 并输出 JSON 转写事件。TTS 服务接收 HTTP POST 并返回原始 PCM。

## 协议参考

### WHIP 信令

信令遵循 [RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html)。

| 步骤 | 方法 | 路径 | 请求体 | 响应 |
|------|--------|------|------|----------|
| 1 | `POST` | `/whip` | SDP offer（`application/sdp`） | `201 Created`，带 SDP answer、`Location: /whip/{sessionId}` 和 `ETag` |
| 2 | `DELETE` | `/whip/{sessionId}` | 无 | `200 OK` |
| — | `OPTIONS` | `/whip` 或 `/whip/{sessionId}` | 无 | `204 No Content`，带 `Accept-Post: application/sdp` |

`POST /whip` 按客户端 IP 做限流（每分钟 30 个会话）。超限时服务端返回 `429 Too Many Requests` 并带 `Retry-After` 头。每次 POST 都会建立一个 peer 连接并收集 ICE，所以即使关闭了鉴权，该端点仍然限流。

客户端创建 SDP offer，收集 ICE 候选，然后 `POST` 到 `/whip`。服务端创建 peer，收集自己的候选，并返回带服务端生成 session ID 的 answer。不用 trickle ICE，也没有常驻信令连接。

本实现与 WHIP 核心流程一致：以 `application/sdp` 发起 `POST`，`201 Created` 返回 answer，`Location` 给出会话 URL，`ETag` 标识 ICE 会话，`DELETE` 拆除，`OPTIONS` 返回 `Accept-Post`，双端完整 ICE 收集。音频为 `sendrecv`，并有一条用于双向事件的 DataChannel。

### 实时事件

客户端必须在生成 offer 之前创建一条标签为 `events` 的 DataChannel。服务端会发送：

| 类型 | 载荷 | 说明 |
|------|---------|-------------|
| `transcript` | `{ "type": "transcript", "text": string, "final": boolean }` | 用户转写更新 |
| `response` | `{ "type": "response", "text": string }` | 流式回复文本 |
| `state` | `{ "type": "state", "state": "listening" \| "thinking" \| "speaking" }` | 智能体轮次状态，用于 UI 指示 |
| `timing` | `{ "type": "timing", "stage": string, "ms": number }` | `pipeline.debug = true` 时的延迟耗时 |

目前的耗时阶段：`llm_first_token`、`tts_first_byte`。

客户端在同一条通道上发送的消息会被路由进流水线 —— 当前用于 `vision.analyze` 插件消费的摄像头图像分片。

### 鉴权

设置 `server.jwt_secret` 即要求 `/whip` 携带 `Authorization: Bearer <jwt>`。设置后，服务端还会暴露 `POST /token`，签发有效期一小时的 HS256 token。设置 `server.api_key` 则要求 `POST /token` 本身携带 `Authorization: Bearer <api_key>`，这样只有你的后端能签发会话 token。两者默认为空，即关闭鉴权。

## 配置

从 [`config.toml.example`](./config.toml.example) 开始：

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

注意：

- `server.public_ip` 加上 `server.turn_secret` 会启用内置的 Pion STUN/TURN 服务器，替代外部 coturn 容器。TURN 监听 UDP 与 TCP 3478，并在 UDP 50001–60000 上中继媒体。
- `plugins.directory` 是加载插件和技能的必要项；省略它则跳过发现流程。
- `pipeline.barge_in` 允许用户在智能体说话时打断。说话人一开口，智能体音频立即压低；若判定只是回应词则恢复。
- `pipeline.greeting` 在会话连接时播放。呼出 SIP 通话在设置了 `pipeline.greeting_outgoing` 时使用后者。
- `pipeline.debug = true` 会通过 DataChannel 发出耗时事件，并记录按轮次的延迟拆解。
- `pipeline.turn_merge_ms` 是一个 final 转写被保留多久以便后续内容合并进同一轮次。如果智能体在说话人话说到一半时就作答，调高它；如果回复感觉迟钝，调低它。当文本在句中或悬空词处结束时，等待会自动延长。
- `pipeline.user_speech_quiet_ms` 是说话人必须安静多久之后智能体才开口。
- `pipeline.rag_prefetch` 让检索与轮次合并窗口重叠。默认关闭；它会发出一次投机式 embedding + 检索，若轮次文本发生变化则丢弃。
- `pipeline.readback_bargein_guard_enabled` 防止微弱的更正和回应词打断确认性复述。只有明确的命令（stop、cancel、hang up）才会打断。默认关闭。
- `deepgram.endpointing` 和 `deepgram.utterance_end_ms` 调节上游何时认为一轮结束；轮次合并去抖运行在它们之上。
- `deepgram.tts_model` 选择 Aura 音色；STT（`model`）和 TTS（`tts_model`）共用同一个 API key。音色命名为 `[family]-[voice]-[language]` —— 见 [Deepgram 音色列表](https://developers.deepgram.com/docs/tts-models)。
- `cartesia.max_concurrency` 应与你套餐的 TTS 并发上限一致 —— Cartesia 统计的是进行中的生成任务而非通话数，超限会返回 429。

## 架构与实现

```text
┌─────────────────────┐                    ┌─────────────────────────────────────┐
│  Client / SDK / SIP │                    │      StreamCore Runtime (Go)        │
│                     │                    │                                     │
│  Mic → WebRTC ──────┼──── Opus RTP ──────┼──→ Opus decode → VAD → STT          │
│  Speaker ← WebRTC ←─┼──── Opus RTP ←─────┼──← Opus encode ← TTS                │
│                     │                    │               │                     │
│  HTTP POST ─────────┼── WHIP (SDP) ──────┼──→ Peer + session created           │
│  DataChannel ◄──────┼──── events   ←─────┼──← transcript · response · state    │
│                     │                    │               │                     │
│                     │                    │               ├── your LLM client   │
│                     │                    │               ├── RAG context       │
│                     │                    │               ├── Skills prompt     │
│                     │                    │               ├── Plugin runtime    │
│                     │                    │               │   ├── Python        │
│                     │                    │               │   ├── TypeScript    │
│                     │                    │               │   └── JavaScript    │
│                     │                    │               └── Native Go tools   │
└─────────────────────┘                    └─────────────────────────────────────┘
```

**媒体流。** 麦克风音频经 WebRTC 到达，解码为 20 ms 帧的 PCM，过一遍 VAD，然后流式送入 STT。最终转写交给模型层；流式输出按句边界切分并随到随交给 TTS，于是合成先于生成完成就已开始。合成出的 PCM 被重新编码为 Opus 并写入 RTP 流。转写、回复和状态文本则并行地走 DataChannel。

**为什么用 Go。** 对延迟敏感的那条链路用 Go 加 [Pion](https://github.com/pion/webrtc) 实现：每个阶段一个 goroutine，阶段之间用有界 channel 连接，热循环中没有 GC 密集的缓冲。RTP 读取、Opus 解码、VAD、STT 流、编排、TTS、Opus 编码和 RTP 写入各自独立成一个阶段。这是为可预测的轮次延迟服务的实现选择 —— 你实际编程面对的接口是 SDK 和事件协议，语言随你喜欢。

**包结构**

| 包 | 职责 |
|---------|----------------|
| [`internal/signaling`](./internal/signaling/) | WHIP handler、SDP 交换、会话 URL |
| [`internal/peer`](./internal/peer/) | Pion peer 连接、track、DataChannel |
| [`internal/session`](./internal/session/) | 会话管理器、多 peer 生命周期 |
| [`internal/pipeline`](./internal/pipeline/) | 入站/出站音频、智能体循环、插话打断、思考音 |
| [`internal/audio`](./internal/audio/) | Opus 编解码、RTP 分帧 |
| [`internal/vad`](./internal/vad/) | 基于能量的语音活动检测 |
| [`internal/stt`](./internal/stt/)、[`internal/tts`](./internal/tts/)、[`internal/llm`](./internal/llm/) | 服务商适配器 |
| [`internal/plugin`](./internal/plugin/) | 插件运行时、原生工具、技能 |
| [`internal/rag`](./internal/rag/) | 检索与 embedding |
| [`internal/turn`](./internal/turn/) | 内置 STUN/TURN 服务器 |

## SDK 与示例

客户端 SDK：

- TypeScript：`@streamcore/js-sdk`
- React Native / Expo：`@streamcore/react-native-sdk`（尚未发布到 npm）
- Python：`streamcore`
- Go：`github.com/streamcoreai/go-sdk`
- [Rust](https://github.com/streamcoreai/rust-sdk)

插件 SDK：`@streamcore/plugin`（TypeScript）、`streamcore-plugin`（Python）。

示例：

- [TypeScript 浏览器应用](https://github.com/streamcoreai/examples/tree/main/typescript)
- [Go CLI 示例](https://github.com/streamcoreai/examples/tree/main/golang)
- [Go TUI 示例](https://github.com/streamcoreai/examples/tree/main/golang-tui)
- [Python 示例](https://github.com/streamcoreai/examples/tree/main/python)
- [Rust CLI 示例](https://github.com/streamcoreai/examples/tree/main/rust)
- [Rust TUI 示例](https://github.com/streamcoreai/examples/tree/main/rust-tui)

## 路线图

尚未实现 —— 在此列出，以保证上面的能力表格诚实：

- **水平扩展。** 会话状态在内存中且单进程。今天的多实例部署需要粘性路由或外部会话协调。
- **会话重连。** 没有 ICE restart 或恢复路径；连接断开就意味着一个新会话。
- **指标与可观测性。** 已有 `/health` 和 DataChannel 耗时事件；尚无 Prometheus/OpenTelemetry 导出。
- **HTTP 智能体端点。** 可配置的 OpenAI 兼容或 webhook 式智能体后端，让「自带智能体」无需写 Go 代码。
- **跨会话的持久记忆。**
- **更多示例** 以印证定位：实时翻译器、AI 主持的语音房间、浏览器副驾、嵌入式设备、SIP 应用，以及一个完全不含 LLM 的纯音频处理应用。
- **嵌入式客户端加固。** [`esp32`](https://github.com/streamcoreai/esp32) 中的 ESP32-S3 固件能通过 WHIP 连接，但尚未达到生产可用。

## Star 历史

<!-- star-history:start -->
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/star-history/star-history-dark.svg">
  <img alt="Star history" src="assets/star-history/star-history-light.svg">
</picture>
<!-- star-history:end -->

## 许可证

Apache 2.0。见 [LICENSE](./LICENSE)。
