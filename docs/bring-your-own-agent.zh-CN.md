[English](./bring-your-own-agent.md) | **简体中文**

# 接入你自己的智能体

大多数智能体框架从提示词、工具与模型编排开始。StreamCore 从更下面一层开始：实时媒体链路 —— 传输、编解码、流式语音、轮次控制、插话打断、网络穿透、会话状态，以及与 AI 服务之间的通信。

你的智能体不必住在 StreamCore 里。有四种受支持的方式让智能体始终归你。

## 1. 把智能体放在一次工具调用之后

插件（Python、TypeScript、JavaScript，走 JSON-RPC）与原生 Go 工具让对话可以调用你已有的后端 —— 你的 API、你的编排、你的数据。StreamCore 负责把结果以语音流式播报出去。

完整示例见[快速开始 → 接入你自己的后端](./quickstart.zh-CN.md#接入你自己的后端)。

## 2. 把模型层指向你自己的基础设施

`llm.provider = "ollama"` 配合自定义的 `base_url`，可以指向任何你运行的、兼容 Ollama 的端点，包括你自己做路由或模型编排的那一层。

```toml
[llm]
provider = "ollama"

[ollama]
base_url = "https://models.internal.example.com"
model = "your-model"
```

## 3. 直接实现 LLM 接口

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

## 4. 使用 StreamCore 可选的内置智能体运行时

如果你需要，LLM 编排、工具、技能、RAG 与对话历史都是开箱即用的。见[智能体运行时](./agent-runtime.zh-CN.md)。

---

一个无需写 Go 代码的通用 HTTP / 兼容 OpenAI 的智能体端点还在[路线图](./roadmap.zh-CN.md)上；今天的方式 3 只是一个小文件，而不是 fork 整个项目。
