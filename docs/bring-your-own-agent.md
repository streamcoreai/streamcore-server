**English** | [简体中文](./bring-your-own-agent.zh-CN.md)

# Bring your own agent

Most agent frameworks start with prompts, tools, and model orchestration. StreamCore starts one layer lower: the realtime media path — transport, codecs, speech streaming, turn-taking, interruption, network traversal, session state, and communication with AI services.

Your agent does not have to live inside StreamCore. There are five supported ways to own the intelligence.

## 1. Keep your agent behind a tool call

Plugins (Python, TypeScript, JavaScript over JSON-RPC) and native Go tools let the conversation call into your existing backend — your APIs, your orchestration, your data. StreamCore streams the result back as speech.

Worked example: [Quick start → Connect your own backend](./quickstart.md#connect-your-own-backend).

## 2. Point each turn at your own HTTP agent

`llm.provider = "agent"` POSTs every user turn to an endpoint you host — any language, any framework, no Go code. The endpoint owns the conversation (memory, prompting, tool use), keyed by the `session_id` sent with each request; StreamCore streams the reply into TTS sentence by sentence, and barge-in cancels the in-flight request.

```toml
[llm]
provider = "agent"

[agent]
url = "http://localhost:9000/agent"
api_key = ""            # sent as Authorization: Bearer; empty disables auth
timeout_ms = 60000      # whole-turn budget, including streaming the reply
```

Each request is JSON:

```json
{
  "session_id": "9f2c…",
  "type": "chat",
  "text": "what the caller said",
  "system": "skill text appended by the server, if any"
}
```

`session_id` is stable for the conversation and rotates on reset. `type` is `"chat"` for a user turn, or `"oneshot"` for stateless background transforms such as the rolling summary (then `system` and `text` carry the transform's prompt pair).

### What your agent sends back

Any 2xx response carrying text, in one of three shapes — StreamCore dispatches on the reply's `Content-Type`. A non-2xx status fails the turn (the caller hears nothing) and the error body is logged.

**`application/json` — simplest, buffered.** Return the whole reply at once; nothing is spoken until it is complete:

```http
HTTP/1.1 200 OK
Content-Type: application/json

{"text": "It's 22 degrees and sunny."}
```

`{"response": "…"}` is accepted as an alias for `text`.

**`text/plain` — streamed, lowest effort.** Write and flush as you generate; StreamCore speaks each complete sentence while the rest is still arriving:

```http
HTTP/1.1 200 OK
Content-Type: text/plain
Transfer-Encoding: chunked

It's 22 degrees and sunny. Want tomorrow's forecast as well?
```

**`text/event-stream` — streamed, SSE.** The natural fit if your agent already relays an LLM SDK's stream. Each `data:` line is either a JSON object `{"delta": "…"}` or raw text; an optional `data: [DONE]` line ends the stream:

```http
HTTP/1.1 200 OK
Content-Type: text/event-stream

data: {"delta": "It's 22 degrees "}

data: {"delta": "and sunny."}

data: [DONE]
```

Whichever shape you pick, the concatenated text is exactly what the caller hears — reply with plain conversational words, not markdown. `"oneshot"` requests are answered the same way (buffered JSON is fine there; the result is used internally, never spoken). A complete agent is ~10 lines of Flask or Express: read `text`, look up `session_id`, return words.

## 3. Point the model layer at your own infrastructure

`llm.provider = "ollama"` with a custom `base_url` targets any Ollama-compatible endpoint you run, including one that fronts your own routing or model stack.

```toml
[llm]
provider = "ollama"

[ollama]
base_url = "https://models.internal.example.com"
model = "your-model"
```

## 4. Implement the LLM interface directly

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

## 5. Use StreamCore's optional built-in agent runtime

LLM orchestration, tools, skills, RAG, and conversation history ship in the box if you want them. See [Agent runtime](./agent-runtime.md).

---

Option 2 covers the no-Go-code case with a small JSON contract; an OpenAI-compatible endpoint mode is still on the [roadmap](./roadmap.md).
