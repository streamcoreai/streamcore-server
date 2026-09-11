[English](./developer-agent.md) | **简体中文**

# 开发者智能体

两个可选集成，让语音对话能够触达真实代码库：GitHub 负责读取 CI 与创建 Pull
Request，Codex 负责在隔离检出中排查并修复问题。

```text
用户：“StreamCore 的 CI 为什么失败了？”
   → github.latest_ci_failure → 精简后的日志片段 → 智能体解释原因

用户：“让 Codex 调查一下。”
   → 隔离 worktree → Codex 线程 → 根因 → 智能体解释

用户：“修复它。”     → 确认 → Codex 编辑 worktree 并运行测试
用户：“开个 PR。”    → 确认 → 推送分支，创建 Pull Request
```

两者默认关闭。任何一个都不会导致服务器启动失败，也都不接触媒体路径：一次耗时四
分钟的 Codex 任务不会让任何一帧音频延迟。工具返回普通的结构化 JSON —— 由智能体
转成语音，由既有的 display projector 把答案渲染成卡片。

---

## GitHub

### 认证

使用 **GitHub App**，而不是个人访问令牌。App 私钥被换取为安装访问令牌，有效期一
小时，且被限定到单个仓库与单个权限集：

```text
app_id + 私钥  →  RS256 JWT（9 分钟）
               →  POST /app/installations/{id}/access_tokens
               →  安装令牌（1 小时，单仓库，最小权限）
               →  GitHub API
```

令牌按仓库**并且**按权限集缓存，因此读路径永远拿不到写令牌；过期前十分钟会重新
签发。私钥在启动时读取一次，此后不会离开进程 —— 不会进入提示词、工具结果、
DataChannel、日志行或测试快照。

### 权限

两组，按操作分别申请：

| 操作 | 权限 | 用途 |
|---|---|---|
| 读取（排查） | `actions:read`、`contents:read`、`pull_requests:read`、`checks:read`、`metadata:read` | 工作流运行与作业日志；文件与提交读取；PR 读取；check-run 注解 |
| 写入（PR） | `contents:write`、`pull_requests:write`、`metadata:read` | 推送分支、创建 Pull Request |

永远不会申请：`administration`、`secrets`、`members`、`actions:write`、
`workflows`。本阶段不修改工作流文件，也不做仓库管理。

### 仓库访问

两个条件缺一不可：

1. 仓库列在 `github.repositories` 中
2. App 安装确实能访问它

配置表达的是意图，安装才是真正的授权。访问权通过为该仓库签发读令牌来验证 ——
GitHub 拒绝把令牌限定到安装无法看到的仓库 —— 结果会被缓存，直到 GitHub 返回 401
或 404。

### 工具

| 工具 | 作用 |
|---|---|
| `github.latest_ci_failure` | 最近一次失败的运行，含失败作业、失败步骤、注解与精简日志片段 |
| `github.repo_status` | 默认分支、开放 issue 数、最新运行状态 |
| `github.workflow_run` | 列出最近的运行，或按 id 描述某一次 |
| `github.workflow_logs` | 某次运行或某个作业的精简日志 |
| `github.pull_request` | 读取单个 Pull Request |
| `github.commit` | 读取单个提交 |
| `github.file` | 按 ref 读取单个文件 |
| `github.diff` | 两个 ref 之间的 unified diff |
| `github.create_pull_request` | **需确认。** 推送开发任务的分支并创建 Pull Request |

### 日志精简

原始作业日志动辄数 MB，其中几乎没有解释失败的内容。在任何内容抵达模型之前，日志
会被确定性地精简：

- 去掉 GitHub 的逐行时间戳与 ANSI 转义
- 命中失败信号的行会锚定一段上下文窗口 —— `FAIL`、panic、编译器
  `file:line:col:` 消息、断言与 expected/actual 输出、`##[error]`、非零退出码、
  堆栈跟踪
- 已识别的噪声即使落在窗口内也会被丢弃：依赖下载、进度百分比、`PASS`/`ok` 行、
  分组标记
- 重复超过两次的行会被折叠
- 结果上限为 **60 行 / 4000 字节**，从头部裁剪，因为 CI 是在失败结束之后才写出
  汇总的

同一份日志总是精简出同样的片段，因此一次失败的对话无需重跑 CI 即可复现。

---

## Codex

### 认证

Codex 使用**你的 ChatGPT 订阅**登录，走官方的 “Sign in with ChatGPT” 流程。
StreamCore 不持有这些凭据：

- `[codex]` 中没有 `api_key`，也不会读取 `OPENAI_API_KEY`
- API key 账户会被报告为*未认证*，不会作为回退使用
- 当 Codex 请求 StreamCore 提供刷新后的 ChatGPT 令牌时，StreamCore 拒绝 ——
  Codex 自己拥有其凭据状态
- `codex.status` 只报告可用性、套餐类型与忙碌状态，除此之外什么都不报告：没有
  令牌、没有账户 id、没有凭据路径

以将要运行 StreamCore 的那个操作系统用户身份登录一次：

```bash
codex login          # 无图形界面的主机加上 --device-auth
codex login status   # 期望输出：Logged in using ChatGPT
```

Codex 把该状态保存在它自己的 home 里（默认 `~/.codex`）。如果 StreamCore 以专用
服务账户运行，登录必须**以该用户身份**完成 —— root 的登录对其他用户无效。

### 固定供应商很重要

`model_provider` 与 `model` 通过 Codex 命令行传入，而不是交给
`~/.codex/config.toml`。如果你的 Codex 默认值指向第三方供应商，未固定的
StreamCore 会把每个开发任务都跑在那里，且完全不会用到你的 ChatGPT 套餐 —— 悄无
声息。

模型必须是你的 ChatGPT 套餐允许的。`gpt-5.1-codex` 系列对 ChatGPT 账户会被拒绝，
提示 *“not supported when using Codex with a ChatGPT account”*；经过验证的默认值
是 `gpt-5.6-terra`。

### 进程

一个长期存活的 `codex app-server --stdio` 子进程，通过 JSON-RPC 通信。线程在其上
复用 —— 不会为每个请求启动新进程。关闭时，进行中的 turn 会通过官方的
`turn/interrupt` 方法中断，子进程被干净关闭，不留残余。

### 隔离

Codex 永远不碰服务器自身的工作副本。

```text
<workspace_root>/
  repo-cache/streamcoreai__streamcore-server/   每个仓库一份克隆
  worktrees/task_a1b2c3d4e5f60718/              每个任务一个 git worktree
```

所有路径都由仓库名与任务 id 在内部生成；来自语音或模型输入的路径不会抵达文件系
统。包含性检查会对最深的已存在祖先解析符号链接，因此 `../`、绝对路径、以及埋在
路径中段的符号链接都会被拒绝 —— 删除时同样如此。

克隆由 StreamCore 使用通过 `GIT_ASKPASS` 传入的只读令牌完成，随后 remote 会被重
写为不含凭据的形式。worktree 内部的任何东西都无法向 GitHub 认证。

沙箱随任务而定：

| 工具 | 沙箱 | 审批策略 |
|---|---|---|
| `codex.analyze` | `readOnly` | `never` |
| `codex.fix`、`codex.test` | `workspaceWrite`，可写根 = 本任务的 worktree | `never` |

`never` 意味着 Codex 不会询问，也无法提权。若仍有审批请求到达，一律拒绝：一句
“修复这个失败的测试”授权的是某一个 worktree 内的编辑与测试命令，而 Codex 需要询
问的事情按定义就在它之外。推送、读取宿主机密、访问无关仓库、修改系统配置，全都
够不着。

### 会话与线程

```text
StreamCore 会话 → 开发任务 → Codex 线程 → 隔离 worktree
```

映射以对话为键，因此“让 Codex 调查”“修复它”“跑一下测试”“给我看 diff”都会延续同
一次调查，两位来电者也绝不会共用一个 worktree。

### 事件

Codex 的事件流被精简为少量开发者状态 —— `analyzing`、`editing`、
`running_tests`、`analysis_ready`、`fix_ready`、`failed`、`cancelled` —— 外加改
动的文件、执行的命令、测试是否通过，以及 Codex 自己的最终消息。推理条目会被直接
丢弃，永不抵达语音模型。

### 工具

| 工具 | 作用 |
|---|---|
| `codex.status` | 是否可用、是否已认证、是否忙碌 |
| `codex.analyze` | 只读调查并解释根因 |
| `codex.fix` | **需确认。** 编辑 worktree 并运行测试 |
| `codex.test` | 为当前改动重跑测试 |
| `codex.diff` | 展示当前改动 |
| `codex.cancel` | 中断正在运行的 Codex turn |

没有 shell 工具。Codex 在分配给它的 worktree 内使用自己的工具；语音模型无法执行
命令。

`codex.cancel` 是一条明确的指令，不与语音打断挂钩。抢话不会放弃一个进行了五分钟
的修复。

---

## 确认

`codex.fix` 与 `github.create_pull_request` 在插件清单中设置了
`confirmation_required`，服务器将其实现为两次调用的门禁。第一次调用返回一段供智
能体朗读的提示，不执行任何操作；只有携带服务器签发令牌的第二次调用才会执行。令
牌一次性、五分钟后过期，并绑定到对话以及参数的哈希 —— 因此模型无法伪造、重放、
借用他人的令牌，也无法在提问与回答之间偷换仓库。见
[插件 → 确认](../../docs/plugins.md#confirmation)。

## 隔离边界

```text
                    StreamCore
              /                     \
        GitHub 集成               Codex 集成
             |                        |
      GitHub App 认证           ChatGPT 订阅
```

Codex 拿到的是隔离检出、CI 失败证据和一个任务。它拿不到 GitHub 令牌、私钥，也没
有推送能力。GitHub 一侧拿到的是 worktree 路径、分支、摘要，以及测试是否运行过 ——
拿不到任何 Codex 凭据。两个 Go 包互不 import；两者之间的适配器位于 `main`，因此
日后也不存在可被滥用的 import 边。

创建 Pull Request 在以下任一条件不满足时拒绝：仓库已在白名单且已安装、分支不是受
保护分支（`main`、`master`、`production`、`release`、`trunk`、`develop`）、存在
diff、未触及明显的密钥文件、任务的测试确实运行过。没有合并路径，也不会向默认分
支推送。

## 故障表现

| 情况 | 仍然可用 |
|---|---|
| GitHub 配置有误 | Codex、语音、显示 |
| Codex 未登录 | GitHub、语音、显示 —— Codex 工具返回明确的“请运行 `codex login`”错误 |
| Codex 认证过期 | 下一个 turn 即被发现；Codex 标记为需重新认证，其余不受影响 |
| 两者都关闭 | 服务器行为与本功能出现之前完全一致 |

## 清理

被遗弃的 worktree 每小时清扫一次，在最后一次使用满 24 小时后删除 —— 这个窗口足
以让支撑着一个开放 PR 的 worktree 活过产生它的那次对话。StreamCore 仍持有的任务
永远不会被删除，`workspace_root` 之外的路径无论如何传入都不会被删除。

## 配置步骤

带注释的 `[github]` 与 `[codex]` 配置块见
[`config.toml.example`](../config.toml.example)。

1. 创建一个 GitHub App，赋予上文的读权限（只有需要创建 PR 时才加上那对写权限），
   生成私钥，并安装到你希望可访问的仓库上。
2. 从安装页 URL 或 `GET /repos/{owner}/{repo}/installation` 取得 installation id。
3. 把 PEM 存放在只有服务器可读的位置（`chmod 600`），并让
   `github.private_key_path` 指向它。
4. 安装 Codex，并以运行 StreamCore 的操作系统用户身份用 ChatGPT 登录。
5. 设置 `github.enabled` 与 `codex.enabled`，然后重启 —— 插件与工具在启动时发现。
