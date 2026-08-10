[English](./agent-runtime.md) | **简体中文**

# 可选的智能体运行时

本页内容全部是可选的。如果你的智能体住在自己的技术栈里，可以整页跳过 —— 见[接入你自己的智能体](./bring-your-own-agent.zh-CN.md)。

当你确实希望由 StreamCore 来跑这段对话时，它会提供带对话历史的 LLM 编排、工具、行为技能与内联检索。

一旦启用内置运行时，有两项行为会自动生效：

- **滚动摘要。** 长通话会超出模型的历史窗口。较早的轮次会在后台被摘要并作为上下文注入，因此第一分钟提到的事实在第十分钟依然有效。
- **低置信度处理。** 当语音识别报告置信度较低时，会提示智能体请对方重复一遍，而不是靠猜；若连续多轮如此则会升级处理。

## 插件与技能

插件赋予智能体**能力**，技能塑造它的**行为**。

- 插件调用 API、数据库、日历、CRM、工作流与内部工具
- 技能定义语气、性格、护栏、品牌调性与流程引导

插件以 Python、TypeScript 或 JavaScript 进程的形式运行，通过 JSON-RPC 通信。技能是注入系统提示词的 Markdown 文件。示例插件与技能在 [`plugins/`](../plugins/) 下。若想完全避免 IPC，可以用 `pluginMgr.RegisterNative(...)` 注册原生 Go 工具。

### 插件清单字段参考

| 字段 | 类型 | 必填 | 说明 |
|-------|------|----------|-------------|
| `name` | string | 是 | LLM 调用时使用的唯一工具名（如 `weather.get`） |
| `description` | string | 是 | 工具的功能说明 —— 会展示给 LLM |
| `version` | int | 是 | 清单版本 |
| `language` | string | 是 | `python`、`typescript` 或 `javascript` |
| `entrypoint` | string | 是 | 要运行的文件（如 `main.py`、`index.ts`） |
| `parameters` | object | 是 | 描述工具参数的 JSON Schema |
| `confirmation_required` | bool | 否 | 执行前让智能体先向用户确认（默认 `false`） |
| `thinking_sound` | bool | 否 | 工具运行期间播放轻柔的循环提示音，有 500 毫秒宽限期（默认 `false`） |

### 内置插件

| 插件 | 语言 | 说明 |
|--------|----------|-------------|
| `math.calculate` | TypeScript | 计算数学表达式 |
| `weather.get` | TypeScript | 查询某地当前天气 |
| `time.get` | Python | 查询任意时区的当前日期/时间 |
| `vision.analyze` | TypeScript | 分析来自设备摄像头的图像 |
| `gmail` | TypeScript | 通过 Gmail 读写邮件（OAuth2）—— 见 [Gmail 插件 README](../plugins/plugins/gmail/README.md) |

### 内置技能

| 技能 | 说明 |
|-------|-------------|
| `tool-savvy` | 引导智能体使用工具而不是猜测 |
| `friendly-conversationalist` | 温暖、自然的对话性格 |
| `polite-assistant` | 简洁而礼貌的语音交互风格 |
| `concise-responder` | 让回复保持简短，适合口头表达 |
| `error-recovery` | 在语音对话中优雅地处理错误 |
| `vision-assistant` | 启用基于摄像头的图像分析 |
| `gmail-assistant` | 逐封处理邮件，带回复与确认流程 |

插件 SDK：`@streamcore/plugin`（TypeScript）、`streamcore-plugin`（Python）。

## 检索（RAG）

RAG 内联运行在媒体流水线中：服务端对用户这一轮做 embedding，从你的向量库检索 top-k 片段，并在调用 LLM 之前注入 —— 只有一次 LLM 调用，没有工具调用的往返。

有两点让检索不落在关键路径上。不含实义词的轮次（「好的」「谢谢」）会被跳过，因为没有可供向量检索锚定的内容。另外，当 `pipeline.rag_prefetch = true` 时，检索会在轮次合并窗口内推测性地开始，于是 embedding 与向量检索的往返与流水线本来就要等待的时间重叠，而不是叠加在它之上。

| 服务商 | 后端 | 配置段 |
|----------|---------|----------------|
| `pgvector` | 启用 pgvector 扩展的 PostgreSQL | `[pgvector]` |
| `supabase` | Supabase（通过 HTTP 调用 Postgres RPC） | `[supabase]` |

两者都使用 OpenAI embedding（默认 `text-embedding-3-small`），因此必须设置 `[openai].api_key`。省略 `[rag]` 段即可完全关闭检索。

### pgvector 配置

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

### Supabase 配置

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

### 文档入库

服务端只负责查询时的检索。向量库的内容由 `streamcore-cli` 填充 —— 那是一个独立的 Go 二进制，会读取本服务的 `config.toml`。

> **`streamcore-cli` 尚未公开。** `github.com/streamcoreai/streamcore-cli` 目前返回 404，因此下面的 clone 会失败。这里记录的参数与行为在它发布时是准确的 —— 在那之前，如果你需要文档入库，请在 [Discord](https://discord.gg/xKGFaGWawT) 里问一声。

```bash
git clone https://github.com/streamcoreai/streamcore-cli
cd streamcore-cli && go build -o streamcore-cli .

# 支持 .txt、.md、.csv、.pdf、.docx、.xlsx
streamcore-cli ingest docs/faq.pdf product-catalog.xlsx notes.md
streamcore-cli ingest --provider supabase --config ../server/config.toml data.csv
streamcore-cli ingest --chunk-size 256 --chunk-overlap 32 manual.docx
```

CLI 会从服务端的 `config.toml` 读取服务商凭据，因此没有任何东西需要配置两遍。

| 参数 | 默认值 | 说明 |
|------|---------|-------------|
| `--config` | 自动探测 | 服务端 `config.toml` 的路径 |
| `--provider` | 取自配置 | 覆盖 RAG 服务商（`pgvector`、`supabase`） |
| `--chunk-size` | 512 | 目标分块大小（词数） |
| `--chunk-overlap` | 64 | 分块之间的重叠（词数） |
