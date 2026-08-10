**English** | [简体中文](./bring-your-own-agent.zh-CN.md)

# Bring your own agent

Most agent frameworks start with prompts, tools, and model orchestration. StreamCore starts one layer lower: the realtime media path — transport, codecs, speech streaming, turn-taking, interruption, network traversal, session state, and communication with AI services.

Your agent does not have to live inside StreamCore. There are four supported ways to own the intelligence.

## 1. Keep your agent behind a tool call

Plugins (Python, TypeScript, JavaScript over JSON-RPC) and native Go tools let the conversation call into your existing backend — your APIs, your orchestration, your data. StreamCore streams the result back as speech.

Worked example: [Quick start → Connect your own backend](./quickstart.md#connect-your-own-backend).

## 2. Point the model layer at your own infrastructure

`llm.provider = "ollama"` with a custom `base_url` targets any Ollama-compatible endpoint you run, including one that fronts your own routing or model stack.

```toml
[llm]
provider = "ollama"

[ollama]
base_url = "https://models.internal.example.com"
model = "your-model"
```

## 3. Implement the LLM interface directly

The model layer is a small Go interface in [`internal/llm/llm.go`](../internal/llm/llm.go):

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

Implement it against your agent server, register it in `NewClient`, and the entire media path — transport, VAD, barge-in, TTS chunking, events — works unchanged.

## 4. Use StreamCore's optional built-in agent runtime

LLM orchestration, tools, skills, RAG, and conversation history ship in the box if you want them. See [Agent runtime](./agent-runtime.md).

---

A generic HTTP / OpenAI-compatible agent endpoint that requires no Go code is on the [roadmap](./roadmap.md); today option 3 is a small file, not a fork.
