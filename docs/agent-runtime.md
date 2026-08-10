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

Plugins run as Python, TypeScript, or JavaScript processes over JSON-RPC. Skills are Markdown files injected into the system prompt. Sample plugins and skills live under [`plugins/`](../plugins/). For zero-IPC extensions, register native Go tools with `pluginMgr.RegisterNative(...)`.

### Plugin manifest reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique tool name the LLM calls (e.g. `weather.get`) |
| `description` | string | yes | What the tool does — shown to the LLM |
| `version` | int | yes | Manifest version |
| `language` | string | yes | `python`, `typescript`, or `javascript` |
| `entrypoint` | string | yes | File to run (e.g. `main.py`, `index.ts`) |
| `parameters` | object | yes | JSON Schema describing the tool's parameters |
| `confirmation_required` | bool | no | Agent asks the user to confirm before executing (default `false`) |
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

### Supabase setup

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

### Ingesting documents

The server handles query-time retrieval only. Populate your vector store with `streamcore-cli`, a separate Go binary that reads this server's `config.toml`.

> **`streamcore-cli` is not public yet.** `github.com/streamcoreai/streamcore-cli` currently returns 404, so the clone below will fail. The flags and behaviour documented here are accurate for when it ships — until then, ask in [Discord](https://discord.gg/xKGFaGWawT) if you need document ingestion.

```bash
git clone https://github.com/streamcoreai/streamcore-cli
cd streamcore-cli && go build -o streamcore-cli .

# Supports .txt, .md, .csv, .pdf, .docx, .xlsx
streamcore-cli ingest docs/faq.pdf product-catalog.xlsx notes.md
streamcore-cli ingest --provider supabase --config ../server/config.toml data.csv
streamcore-cli ingest --chunk-size 256 --chunk-overlap 32 manual.docx
```

The CLI reads your server's `config.toml` for provider credentials, so nothing is configured twice.

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | auto-detected | Path to server `config.toml` |
| `--provider` | from config | Override RAG provider (`pgvector`, `supabase`) |
| `--chunk-size` | 512 | Target chunk size in words |
| `--chunk-overlap` | 64 | Overlap between chunks in words |
