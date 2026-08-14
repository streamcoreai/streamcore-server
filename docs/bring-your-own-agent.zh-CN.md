[English](./bring-your-own-agent.md) | **简体中文**

# 接入你自己的智能体

大多数智能体框架从提示词、工具与模型编排开始。StreamCore 从更下面一层开始：实时媒体链路 —— 传输、编解码、流式语音、轮次控制、插话打断、网络穿透、会话状态，以及与 AI 服务之间的通信。

你的智能体不必住在 StreamCore 里。有五种受支持的方式让智能体始终归你。

## 1. 把智能体放在一次工具调用之后

插件（Python、TypeScript、JavaScript，走 JSON-RPC）与原生 Go 工具让对话可以调用你已有的后端 —— 你的 API、你的编排、你的数据。StreamCore 负责把结果以语音流式播报出去。

完整示例见[快速开始 → 接入你自己的后端](./quickstart.zh-CN.md#接入你自己的后端)。

## 2. 把每一轮对话交给你自己的 HTTP 智能体

`llm.provider = "agent"` 会把每一轮用户对话 POST 到你托管的端点 —— 任何语言、任何框架，不需要写 Go 代码。对话（记忆、提示词、工具调用）由端点掌控，以每次请求携带的 `session_id` 为键；StreamCore 把回复逐句流入 TTS，插话打断会取消进行中的请求。

```toml
[llm]
provider = "agent"

[agent]
url = "http://localhost:9000/agent"
api_key = ""            # 以 Authorization: Bearer 发送；留空则不鉴权
timeout_ms = 60000      # 单轮总预算，含流式返回回复的时间
```

每次请求都是 JSON：

```json
{
  "session_id": "9f2c…",
  "resource_id": "user_8891",
  "type": "chat",
  "text": "用户这一轮说的话",
  "system": "服务端追加的技能文本（如有）"
}
```

`session_id` 在整段对话中保持不变，重置时轮换。`type` 为 `"chat"` 表示一轮用户对话；`"oneshot"` 表示无状态的后台变换（如滚动摘要），此时 `system` 与 `text` 携带该变换的提示词。

`resource_id` 标识通话里的**是谁**，而 `session_id` 标识的是**哪一通**。用它把记忆划到人身上：只按 `session_id` 划分的话，对方每次打回来 agent 都要从头认识一遍。部署未提供身份时该字段会被整个省略，因此请把「不存在」当作匿名，而不是当作一个空用户。

它来自你的后端签发的 JWT claim，或来自受信任的服务端调用方——[sip-server](../../sip-server/README.zh-CN.md) 会填入对端的电话号码——绝不会来自浏览器。它也会原封不动地跨过重置与重连：对话可以从头开始，但线那头的人没有变。如何签发见[协议 → 通话方身份](./protocol.zh-CN.md#通话方身份)。

### 你的智能体需要怎么回复

任何携带文本的 2xx 响应，三种格式任选其一 —— StreamCore 按响应的 `Content-Type` 分发。非 2xx 状态码会让这一轮失败（来电者听不到任何声音），错误响应体会被记录到日志。

**`application/json` —— 最简单，整体缓冲。** 一次性返回完整回复；全部收到后才开始播报：

```http
HTTP/1.1 200 OK
Content-Type: application/json

{"text": "现在 22 度，天气晴朗。"}
```

`{"response": "…"}` 可作为 `text` 的别名使用。

**`text/plain` —— 流式，最省事。** 边生成边写出并 flush；StreamCore 会在剩余内容还在传输时就逐句播报：

```http
HTTP/1.1 200 OK
Content-Type: text/plain
Transfer-Encoding: chunked

现在 22 度，天气晴朗。要不要也听听明天的预报？
```

**`text/event-stream` —— 流式，SSE。** 如果你的智能体本来就在转发某个 LLM SDK 的流，这是最自然的选择。每个 `data:` 行是 JSON 对象 `{"delta": "…"}` 或纯文本；可选的 `data: [DONE]` 行结束流：

```http
HTTP/1.1 200 OK
Content-Type: text/event-stream

data: {"delta": "现在 22 度，"}

data: {"delta": "天气晴朗。"}

data: [DONE]
```

无论选哪种格式，拼接后的文本就是来电者听到的内容 —— 请返回口语化的纯文字，不要 markdown。`"oneshot"` 请求用同样的方式回复（用缓冲的 JSON 即可，结果只做内部使用、不会播报）。一个完整的智能体只需约 10 行 Flask 或 Express：读取 `text`，按 `session_id` 查会话，返回文字。

**可运行示例：**[`examples/bring-your-own-agent`](../../examples/bring-your-own-agent) 用 Node 与 Python 各实现了一份该端点，均零依赖，涵盖流式返回、插话取消、`oneshot` 处理，以及对话与人两层独立记忆。用 `node agent.mjs` 启动，把 `[agent] url` 指过去，打进来即可。

## 3. 把模型层指向你自己的基础设施

`llm.provider = "ollama"` 配合自定义的 `base_url`，可以指向任何你运行的、兼容 Ollama 的端点，包括你自己做路由或模型编排的那一层。

```toml
[llm]
provider = "ollama"

[ollama]
base_url = "https://models.internal.example.com"
model = "your-model"
```

## 4. 直接实现 LLM 接口

模型层是 [`internal/llm/llm.go`](../internal/llm/llm.go) 中一个很小的 Go 接口：

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

针对你的智能体服务实现它，在 `NewClient` 中注册，整条媒体链路 —— 传输、VAD、barge-in、TTS 分块、事件 —— 都无需改动即可工作。

## 5. 使用 StreamCore 可选的内置智能体运行时

如果你需要，LLM 编排、工具、技能、RAG 与对话历史都是开箱即用的。见[智能体运行时](./agent-runtime.zh-CN.md)。

---

方式 2 用一个很小的 JSON 契约覆盖了无需写 Go 代码的场景；兼容 OpenAI 的端点模式仍在[路线图](./roadmap.zh-CN.md)上。
