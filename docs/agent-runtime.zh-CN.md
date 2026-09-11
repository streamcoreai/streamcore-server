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

插件以进程的形式运行，通过 JSON-RPC 通信——Python、TypeScript、JavaScript、Go 或任何其他语言，因为清单给出的是 argv 而不是语言。技能是注入系统提示词的 Markdown 文件。示例插件与技能在 [`plugins/`](../plugins/) 下。若某个工具只是把参数转成一个发给客户端的数据包，它完全不需要进程：在清单里声明 `dispatch:` 块，由服务端直接发送。参见[插件开发](https://github.com/streamcoreai/streamcore-server/blob/main/docs/plugins.md)。

### 插件清单字段参考

| 字段 | 类型 | 必填 | 说明 |
|-------|------|----------|-------------|
| `name` | string | 是 | LLM 调用时使用的唯一工具名（如 `weather.get`） |
| `description` | string | 是 | 工具的功能说明 —— 会展示给 LLM |
| `version` | int | 是 | 清单版本 |
| `language` | string | 是 | `python`、`typescript` 或 `javascript` |
| `entrypoint` | string | 是 | 要运行的文件（如 `main.py`、`index.ts`） |
| `parameters` | object | 是 | 描述工具参数的 JSON Schema |
| `confirmation_required` | bool | 否 | 让工具在口头确认后才执行 —— 第一次调用返回提示语与一次性令牌，只有携带该令牌的第二次调用才会运行（默认 `false`） |
| `thinking_sound` | bool | 否 | 工具运行期间播放轻柔的循环提示音，有 500 毫秒宽限期（默认 `false`） |

### 内置插件

| 插件 | 语言 | 说明 |
|--------|----------|-------------|
| `math.calculate` | TypeScript | 计算数学表达式 |
| `weather.get` | TypeScript | 查询某地当前天气和未来几天的预报，并发出一张 `weather` 卡片。在它的配置表里设置 `units = "f"` 可以把卡片切到华氏度 |
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
    embedding vector(1536) NOT NULL,
    embedding_model TEXT NOT NULL,
    source TEXT,
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS documents_embedding_model_idx ON documents (embedding_model);
```

`streamcore-cli setup` 会替你建好这一切；这段 SQL 是留给想自己动手的人的。

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

### 嵌入模型与向量宽度

每一行都记录了给它做 embedding 的模型，而向量列的宽度也是按那个模型定的。服务端启动时两边都会检查，用不了的向量库会直接导致启动失败，而不是让检索悄悄返回错误的片段。

| `embedding_model` | 列类型 |
|---|---|
| `text-embedding-3-small`（默认） | `vector(1536)` |
| `text-embedding-ada-002` | `vector(1536)` |
| `text-embedding-3-large` | `vector(3072)` |

光看宽度是不够的。`ada-002` 和 `3-small` 都是 1536 维，用一个写入、用另一个检索，不会报任何错，只会从错误的片段里给出答案。`embedding_model` 就是为此存在的：服务端会去找一行与 `rag.embedding_model` 不一致的记录，找到就拒绝启动。

`pgvector` 的宽度取自列定义，模型检查是一次走索引的查询。Supabase 那边没有可以通过 PostgREST 读到的系统目录，所以两项都靠取行来判断：空表在入库之前无法校验，启动时连不上的项目只记一条警告，不会阻塞启动。

如果你是在 `embedding_model` 这一列出现之前入的库，服务端会告诉你，并打印出这段迁移 SQL：

```sql
ALTER TABLE documents ADD COLUMN embedding_model TEXT;
UPDATE documents SET embedding_model = '<你当初入库用的模型>';
ALTER TABLE documents ALTER COLUMN embedding_model SET NOT NULL;
CREATE INDEX IF NOT EXISTS documents_embedding_model_idx ON documents (embedding_model);
```

那些行到底来自哪个模型，只有你自己知道，所以回填的值留成了占位符。要是已经想不起来了，就重新入库一遍。

### 文档入库

服务端只负责查询时的检索。向量库的内容由 [`streamcore-cli`](https://github.com/streamcoreai/streamcore-cli) 填充 —— 那是一个独立的 Go 二进制。之所以独立，是为了让 PDF、docx、xlsx 的解析依赖不会进到服务端镜像里。

macOS 和 Linux 两种架构的预编译二进制都在[发布页](https://github.com/streamcoreai/streamcore-cli/releases)。如果本机有 Go 工具链：

```bash
go install github.com/streamcoreai/streamcore-cli@latest

# 或者从源码构建
git clone https://github.com/streamcoreai/streamcore-cli
cd streamcore-cli && go build -o streamcore-cli .
```

`streamcore-cli setup` 会依次询问服务商、OpenAI key 和凭据，写入 `~/.streamcore/config.toml`，并按你选的模型所需的向量宽度把表建好。Supabase 会额外问一个项目的 Postgres 直连串 —— PostgREST 能插入数据，但跑不了 DDL；这一项留空，它就把 SQL 打印出来给你贴到控制台的 SQL 编辑器里。

如果你已经有服务端的 `config.toml`，CLI 读的是同一种格式，会回退到服务端那份文件，因此凭据不需要配置两遍。

```bash
streamcore-cli setup

# 支持 .txt、.md、.csv、.pdf、.docx、.xlsx
streamcore-cli ingest docs/faq.pdf product-catalog.xlsx notes.md
streamcore-cli ingest --provider supabase --config ../server/config.toml data.csv
streamcore-cli ingest --chunk-size 256 --chunk-overlap 32 manual.docx
```

配置文件的查找顺序：`--config`、`~/.streamcore/config.toml`、`./config.toml`、`../server/config.toml`。

| 参数 | 默认值 | 说明 |
|------|---------|-------------|
| `--config` | `~/.streamcore/config.toml` | 配置文件路径 |
| `--provider` | 取自配置 | 覆盖 RAG 服务商（`pgvector`、`supabase`） |
| `--chunk-size` | 512 | 目标分块大小（词数） |
| `--chunk-overlap` | 64 | 分块之间的重叠（词数） |

入库和查询必须使用同一个 `embedding_model` —— 每个模型对应哪种列，见[嵌入模型与向量宽度](#嵌入模型与向量宽度)。`ingest` 在写入任何数据之前，会先拿配置里的模型去校验向量库，所以对不上的代价是一次查询，而不是一整张写满了用不了的向量的表。

完整的命令说明、支持的格式和建表 SQL：[streamcore-cli README](https://github.com/streamcoreai/streamcore-cli/blob/main/README.zh-CN.md)。

### 端到端跑一遍

挑一份模型不可能已经知道的内容。你自己的发布说明 PDF 可以，现写一个文本文件也可以。

```bash
cat > closing-hours.md <<'EOF'
惠灵顿工坊每月最后一个周五下午 3 点闭店做维护。
其余的周五都是 6 点闭店。
EOF

streamcore-cli setup                    # 服务商、key、模型，以及建表
streamcore-cli ingest closing-hours.md
```

```
Using config: /Users/you/.streamcore/config.toml
Processing closing-hours.md ...
  Extracted 1 chunks
  Uploaded 1/1 chunks
Done. 1 chunks uploaded to supabase (text-embedding-3-small).
```

让服务端指向同一份配置并启动。它会在启动时读一遍这张表，契约没问题就什么都不说：

```
RAG enabled — provider: supabase
Voice agent server listening on :8080
```

然后接一个客户端上去，问「惠灵顿工坊每月最后一个周五几点闭店？」，答案应该是下午 3 点。入库之前问，或者对着用另一个模型建起来的库问，得到的就是一句没有信息量的场面话 —— 而这正是这套契约要让它变响的那种失败。

### 为什么入库是一个单独的二进制

原因在解析器。PDF、docx、xlsx 会拖进一堆依赖，而服务端在查询时根本用不上它们；服务端镜像是要部署出去的东西，入库工具则是在自己笔记本上跑一次的东西。分开之后，服务端镜像里就不会带着一个永远不会被调用的文档解析器。

代价是多了一个带自己配置的产物 —— 所以 CLI 会去读服务端的 `config.toml` 并回退到它：一份凭据，两个二进制。哪天这一点不再成立了，这个拆分就值得重新讨论。
