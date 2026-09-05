**English** | [简体中文](./agent-runtime.zh-CN.md)

# Optional agent runtime

Everything here is opt-in. Skip this page entirely if your agent lives in your own stack — see [Bring your own agent](./bring-your-own-agent.md).

When you do want StreamCore to run the conversation, it provides LLM orchestration with conversation history, tools, behavioral skills, and inline retrieval.

Two behaviours run automatically once the built-in runtime is in use:

- **Rolling summary.** Long calls outlive the model's history window. Older turns are summarized in the background and injected as context, so a fact from minute one survives into minute ten.
- **Low-confidence handling.** When the speech recogniser reports poor confidence, the agent is told to ask the caller to repeat rather than guess, escalating if it happens on consecutive turns.

## Plugins and skills

Plugins give the agent **capabilities**. Skills shape its **behavior**.

- Plugins call APIs, databases, calendars, CRMs, workflows, and internal tools
- Skills define tone, personality, guardrails, brand voice, and workflow guidance

Plugins run as processes over JSON-RPC — Python, TypeScript, JavaScript, Go, or anything else, since the manifest gives an argv rather than a language. Skills are Markdown files injected into the system prompt. Sample plugins and skills live under [`plugins/`](../plugins/). A tool that only turns its arguments into one packet for the client needs no process at all: it declares a `dispatch:` block and the server sends it directly. See [Plugin development](https://github.com/streamcoreai/streamcore-server/blob/main/docs/plugins.md).

### Lifecycle event plugins

Tool plugins are **called by the LLM**. Lifecycle plugins are **called by StreamCore** when a server event occurs. The two capabilities are deliberately separate: implementing one does not require implementing the other, and an event-only plugin is never advertised to the model as a callable tool.

Native Go plugins can optionally implement:

```go
type EventHandler interface {
    Events() []string
    HandleEvent(context.Context, plugin.PluginEvent) ([]plugin.OutboundEvent, error)
}
```

A plugin subscribes by listing event types under `events:` in its manifest. It receives each event as an `event` request and may answer with an envelope, so an observer can push something to the client without ever becoming a tool the model can see; an event-only plugin declares no tools and stays absent from `Tools()`.

The first built-in lifecycle event is `assistant.response.completed`. It is emitted at most once for a settled logical assistant turn and includes the final user transcript, complete assistant response, and interruption flag. Interrupted, cancelled, superseded, streaming-token, partial-STT, and per-sentence events do not produce a completion event. Dispatch is asynchronous and bounded; a handler failure is logged and never blocks LLM streaming, TTS, playback, or barge-in.

### Display projector

The built-in `display-projector` lifecycle plugin subscribes to `assistant.response.completed` and converts the turn into a v1 `display.card` (see [protocol](./protocol.md#display-cards)). It uses the same LLM provider abstraction as the runtime through `OneShot`, has a conservative local fast path for very short answers, validates layout and field limits before sending, and returns its result through the existing session DataChannel callback. The feature is disabled unless configured:

```toml
[display]
enabled = true
plugin = "display-projector"

[display.projector]
timeout_ms = 3000
fast_path_max_chars = 80
```

The projector is generic display semantics, not a NOTE4C plugin. It contains no hardware model, coordinates, fonts, or rendering choices.

### Plugin manifest reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique tool name the LLM calls (e.g. `weather.get`) |
| `description` | string | yes | What the tool does — shown to the LLM |
| `version` | int | yes | Manifest version |
| `language` | string | yes | `python`, `typescript`, or `javascript` |
| `entrypoint` | string | yes | File to run (e.g. `main.py`, `index.ts`) |
| `parameters` | object | yes | JSON Schema describing the tool's parameters |
| `confirmation_required` | bool | no | Gate the tool behind a spoken confirmation — the first call returns a prompt and a single-use token, and only a second call carrying that token runs (default `false`) |
| `thinking_sound` | bool | no | Plays a soft looping tone while the tool runs, after a 500 ms grace period (default `false`) |

### Included plugins

| Plugin | Language | Description |
|--------|----------|-------------|
| `math.calculate` | TypeScript | Evaluate math expressions |
| `weather.get` | TypeScript | Current weather for a location |
| `time.get` | Python | Current date/time in any timezone |
| `vision.analyze` | TypeScript | Analyze images from a device camera |
| `gmail` | TypeScript | Read and send emails via Gmail (OAuth2) — see [Gmail plugin README](../plugins/plugins/gmail/README.md) |

### Included skills

| Skill | Description |
|-------|-------------|
| `tool-savvy` | Guides the agent to use tools instead of guessing |
| `friendly-conversationalist` | Warm, natural conversational personality |
| `polite-assistant` | Concise and polite voice interaction style |
| `concise-responder` | Keeps responses short for spoken delivery |
| `error-recovery` | Handles errors gracefully in voice conversations |
| `vision-assistant` | Enables camera-based image analysis |
| `gmail-assistant` | Walks through emails one-by-one with reply & confirm flow |

Plugin SDKs: `@streamcore/plugin` (TypeScript), `streamcore-plugin` (Python).

## Retrieval (RAG)

RAG runs inline in the media pipeline: the server embeds the user's turn, retrieves the top-k chunks from your vector store, and injects them before the LLM call — one LLM pass, no tool-call round trip.

Two things keep retrieval off the critical path. Turns with no content-bearing words ("okay, sure, thanks") are skipped, since there is nothing to anchor a vector search on. And with `pipeline.rag_prefetch = true`, retrieval starts speculatively during the turn-merge window, so the embedding and vector-search round trip overlaps a wait the pipeline was doing anyway instead of adding to it.

| Provider | Backend | Config section |
|----------|---------|----------------|
| `pgvector` | PostgreSQL with the pgvector extension | `[pgvector]` |
| `supabase` | Supabase (Postgres RPC over HTTP) | `[supabase]` |

Both use OpenAI embeddings (`text-embedding-3-small` by default), so `[openai].api_key` must be set. Omit the `[rag]` section to disable retrieval entirely.

### pgvector setup

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE documents (
    id SERIAL PRIMARY KEY,
    content TEXT NOT NULL,
    embedding vector(1536) NOT NULL,
    embedding_model TEXT NOT NULL,
    source TEXT,
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS documents_embedding_model_idx ON documents (embedding_model);
```

`streamcore-cli setup` creates all of this for you; the SQL is here for anyone who would rather run it themselves.

```toml
[rag]
provider = "pgvector"

[pgvector]
connection_string = "postgres://user:pass@localhost:5432/mydb"
```

### Supabase setup

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE documents (
    id SERIAL PRIMARY KEY,
    content TEXT NOT NULL,
    embedding vector(1536) NOT NULL,
    embedding_model TEXT NOT NULL,
    source TEXT,
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS documents_embedding_model_idx ON documents (embedding_model);

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

### Embedding model and vector width

Every row records the model that embedded it, and the vector column is sized for that model. Both halves are checked when the server boots, and a store it cannot use is a startup failure rather than retrieval that quietly returns the wrong chunks.

| `embedding_model` | Column type |
|---|---|
| `text-embedding-3-small` (default) | `vector(1536)` |
| `text-embedding-ada-002` | `vector(1536)` |
| `text-embedding-3-large` | `vector(3072)` |

The width alone is not enough. `ada-002` and `3-small` are both 1536 wide, so vectors written by one and searched with the other produce no error at all — just answers drawn from the wrong chunks. That is what `embedding_model` is for: the server looks for a single row disagreeing with `rag.embedding_model` and refuses to start if it finds one.

For `pgvector` the width comes from the column definition and the model check is one indexed query. For Supabase there is no catalog to read over PostgREST, so both come from rows: an empty table stays unverified until something has been ingested, and a project that is unreachable at boot logs a warning instead of blocking startup.

If you ingested before `embedding_model` existed, the server will tell you so and print this migration:

```sql
ALTER TABLE documents ADD COLUMN embedding_model TEXT;
UPDATE documents SET embedding_model = '<the model you ingested with>';
ALTER TABLE documents ALTER COLUMN embedding_model SET NOT NULL;
CREATE INDEX IF NOT EXISTS documents_embedding_model_idx ON documents (embedding_model);
```

Only you know which model those rows came from, which is why the backfill is a placeholder. If you no longer know, re-ingest.

### Ingesting documents

The server handles query-time retrieval only. Populate your vector store with [`streamcore-cli`](https://github.com/streamcoreai/streamcore-cli), a separate Go binary. It stays separate so the PDF, docx and xlsx parsers never end up in the server image.

Prebuilt binaries for macOS and Linux, both architectures, are on the [releases page](https://github.com/streamcoreai/streamcore-cli/releases). With a Go toolchain:

```bash
go install github.com/streamcoreai/streamcore-cli@latest

# or from source
git clone https://github.com/streamcoreai/streamcore-cli
cd streamcore-cli && go build -o streamcore-cli .
```

`streamcore-cli setup` asks for your provider, OpenAI key and credentials, writes `~/.streamcore/config.toml`, and creates the table with the vector width your chosen model needs. For Supabase it asks for the project's direct Postgres connection string, since PostgREST can insert rows but cannot run DDL; leave that blank and it prints the SQL for the dashboard's editor instead.

If you already have a server `config.toml`, the CLI reads the same format and falls back to the server's file, so credentials are never configured twice.

```bash
streamcore-cli setup

# Supports .txt, .md, .csv, .pdf, .docx, .xlsx
streamcore-cli ingest docs/faq.pdf product-catalog.xlsx notes.md
streamcore-cli ingest --provider supabase --config ../server/config.toml data.csv
streamcore-cli ingest --chunk-size 256 --chunk-overlap 32 manual.docx
```

Config is looked up in order: `--config`, `~/.streamcore/config.toml`, `./config.toml`, `../server/config.toml`.

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `~/.streamcore/config.toml` | Path to config file |
| `--provider` | from config | Override RAG provider (`pgvector`, `supabase`) |
| `--chunk-size` | 512 | Target chunk size in words |
| `--chunk-overlap` | 64 | Overlap between chunks in words |

Ingest and query must use the same `embedding_model` — see [Embedding model and vector width](#embedding-model-and-vector-width) for which column each model needs. `ingest` checks the store against the configured model before it writes anything, so a mismatch costs one query rather than a table full of unusable vectors.

Full command reference, supported formats and database DDL: [streamcore-cli README](https://github.com/streamcoreai/streamcore-cli#readme).

### End to end

Pick something the model cannot already know. A PDF of your own release notes works; so does a text file you write on the spot.

```bash
cat > closing-hours.md <<'EOF'
The Wellington workshop closes at 3pm on the last Friday of every month
for maintenance. All other Fridays it closes at 6pm.
EOF

streamcore-cli setup                    # provider, key, model, and the table
streamcore-cli ingest closing-hours.md
```

```
Using config: /Users/you/.streamcore/config.toml
Processing closing-hours.md ...
  Extracted 1 chunks
  Uploaded 1/1 chunks
Done. 1 chunks uploaded to supabase (text-embedding-3-small).
```

Point the server at the same config and start it. It reads the table at boot and says nothing if the contract holds:

```
RAG enabled — provider: supabase
Voice agent server listening on :8080
```

Then connect a client and ask *"when does the Wellington workshop close on the last Friday of the month?"* The answer should be 3pm. Ask before ingesting, or against a store built with a different model, and you get a generic non-answer instead — which is the failure this contract exists to make loud.

### Why ingestion is a separate binary

The parsers are the reason. PDF, docx and xlsx pull in dependencies that the server has no use for at query time, and the server image is something people deploy; the ingestion tool is something they run once from a laptop. Keeping them apart means the server image never carries a document parser it will never call.

The cost is a second artifact with its own config, which is why the CLI reads the server's `config.toml` and falls back to it — one set of credentials, two binaries. If that stops holding, the argument for the split is worth revisiting.
