# AI 用量插件

[English](./README.md) | **简体中文**

用一句话回答"我今天用了多少 AI"，同时把一张一个 agent 一块面板的用量卡片推到通话对端的
屏幕上：上面 Claude Code，下面 Codex，每块面板都有五小时窗口和一周窗口两条刻度。

```text
 AI USAGE                          14:32
┌────────────────────────────────────────┐
│ CLAUDE CODE                as of 06:11 │
│ 5H     74% left  ██████████░░░  @02:20 │
│ 7D     48% left  ███████░░░░░░  @Sun   │
│ FABLE  84% left  ████████████░  @Sun   │
└────────────────────────────────────────┘
┌────────────────────────────────────────┐
│ CODEX                      as of 06:08 │
│ 5H    100% left  █████████████  @06:07 │
│ 7D     92% left  ████████████░  @Sep15 │
└────────────────────────────────────────┘
```

插件发出去的是**已用**百分比，面板画的是**还剩**多少，并且把这个词写出来。Claude Code 的
`/usage` 写的是 "26% used"，Codex CLI 写的是 "100% remaining"——所以一个不带说明的百分比，
读的人只能猜它的方向。

说一句"给我看看我的 AI 用量"，模型就会调用 `ai.usage`。这个工具会把拿到的每个数字都念出来，
同时发出一个使用 `usage` 布局的 `display.card` v1 数据包。

> **`usage` 是新增布局。** 本仓库里的
> [zectrix-note4c](../../../../examples/zectrix-note4c) 固件已经能渲染它，但仍然跑着旧固件的
> 设备会把这张卡片当作未知布局拒掉，屏幕上保持原样。想看到画面变化，先重新烧录固件。

## 数字从哪里来

这两个工具都没有把自己的限额写进一个属于自己的文件，所以每个 agent 都有一条实时路径和一条
捡日志的路径。先走实时的；捡来的那条留着兜底——旧数字也比没有数字强，只要它明说自己是旧的。

| Agent | 实时 | 兜底 |
| ----- | ---- | ---- |
| Claude Code | `~/.claude/usage-snapshot.json`，由状态栏包装脚本写出 | 旧的 `eink-snapshots/*.json` |
| Codex | `codex app-server` → `account/rateLimits/read` | 从 `~/.codex/sessions/**/rollout-*.jsonl` 里捡出的 `rate_limits` |

**Codex 是真正实时的。** 它的 CLI 自带一个本地 app-server，`account/rateLimits/read` 返回的是
当前数字——它是去问了一次，而不是报告某次历史会话碰巧看到的值。三行 JSON-RPC 走 stdio，大约
一秒：

```text
-> {"id":1,"method":"initialize","params":{"clientInfo":{…}}}
-> {"method":"initialized","params":{}}
-> {"id":2,"method":"account/rateLimits/read","params":{}}
<- {"id":2,"result":{"rateLimits":{
     "primary":  {"usedPercent":0,"windowDurationMins":300,"resetsAt":1789141035},
     "secondary":{"usedPercent":8,"windowDurationMins":10080,"resetsAt":1789454135},
     "planType":"plus"}}}
```

这一步用的是 Codex 自己存的凭证；本插件既不读取也不转发任何 token。两个窗口按
`windowDurationMins` 的长短来区分，而不是按它们出现在 primary 还是 secondary——这两个位置
以前调换过。

**Claude Code 问不到。** 它只把套餐用量百分比通过 stdin 交给状态栏，别处都没有：没有自己的
文件，没有本地查询接口，没有 `claude usage` 子命令，会话记录里也没有。所以这个数字最新只能
到"上一次状态栏渲染"的时刻，而且必须由某个能看到那份 stdin 的东西把它写到磁盘上。在相信面板
之前，有两件事需要知道：

- **必须配置状态栏**，而且这个状态栏要顺手写出快照（claude-hud 的
  `display.externalUsageWritePath` 就是干这个的）。
- **不渲染状态栏的环境什么都不会产生。** VS Code 扩展就是这个坑：它根本不会执行 `statusLine`，
  所以在那里再怎么用，快照也一直没人动过，卡片就会不声不响地显示上周的读数。开一个终端会话
  （任何会话、任何仓库）就会重新喂上数据；给 `statusLine` 加上 `refreshInterval`，空闲的
  终端会话也会按时钟继续刷新。

写快照的那一方应当**只在数字发生变化时才重写文件**，其余情况保持 `updated_at` 不动。每次刷新
都重新盖一个时间戳，会让一个根本没动过的读数看起来是最新的，下面那套 stale 标记也就白做了。

第二种情况正是 `stale` 存在的理由：超过 `stale_after_minutes` 的读数在面板上写的是
`stale Sep 4` 而不是 `as of 06:11`，口播还会补上这份数据有多旧。完全没有数据的来源写的是
`no recent runs`，不会去蹭另一个 agent 的新鲜度。

这里用绝对时间而不是相对时间，是因为面板可能一个小时都不刷新一次：`as of 06:11` 挂在屏幕上
依然成立，`2m ago` 不成立。

数据源没有回答的窗口发的是 `null` 而不是 `0`，面板会把它画成斜纹而不是空槽——"没读到"和
"没用过"不是一回事。

## 配置

默认关闭，因为这些路径只在真正跑过这两个工具的机器上才存在。

```toml
[plugins.config."ai.usage"]
enabled = true

# 以下均可选，这些就是默认值。
claude_usage_file   = "~/.claude/usage-snapshot.json"           # 实时的那个
claude_snapshot_dir = "~/.claude/plugins/claude-hud/eink-snapshots"
codex_live          = true       # 先问 Codex CLI，再退回读日志
codex_command       = "codex"    # 如果它不在服务端的 PATH 里
codex_timeout_ms    = 6000       # 实时查询大约需要一秒
codex_sessions_dir  = "~/.codex/sessions"
codex_scan_files    = 8
stale_after_minutes = 30         # 超过这个时长，屏幕上会标成 stale
```

`claude_usage_file` 必须和你的状态栏写快照的位置一致。claude-hud 那边这个设置叫
`display.externalUsageWritePath`；任何写出同样形状的包装脚本都可以：

```json
{
  "updated_at": "2026-09-11T09:58:00.000Z",
  "five_hour": { "used_percentage": 62, "resets_at": "2026-09-11T14:10:00Z" },
  "seven_day": { "used_percentage": 17, "resets_at": "2026-09-15T19:00:00Z" }
}
```

把 `codex_live` 设为 `false` 就完全不碰 CLI，只读 rollout 日志——没装 Codex 的服务器应该这样配。

如果你的状态栏把旧格式的按会话快照写在别处，把 `claude_snapshot_dir` 指过去即可。读取器期望的
JSON 形状是：

```json
{
  "timestamp": "2026-09-06T11:50:00Z",
  "usage": {
    "fiveHour": 62,
    "sevenDay": 17,
    "fiveHourResetAt": "2026-09-06T14:00:00Z",
    "sevenDayResetAt": "2026-09-08T00:00:00Z"
  }
}
```

没有 `usage` 对象的文件会被跳过，这样一个还没见过限额数据的状态栏不会被当成"用量为零"。

## 这张卡片

用的是 `usage` 布局：一个标题，下面一个 agent 一块面板，每块面板里一个窗口一条刻度。插件发的是
数字而不是图形——`{"label": "5H", "percent": 62, "reset": "@14:10"}`——62% 长什么样由客户端
自己决定。在 note4c 上它是一条填充的进度条；换一块更大的屏幕，它可以是别的样子。

上限定义在 `note4c-logic/src/display_card.rs`，插件这边照抄同一组数字：最多三个 agent，名字
18 字符、备注 14 字符，每个 agent 最多三条刻度，标签 3 字符、重置时间戳 8 字符。这是 400 px 宽
的面板在 10 px 字宽下，还能隔着房间看清的极限。

还有两点从数据形状上看不出来：

- **`percent` 可以是 `null`。** `null` 表示数据源没给出这个数字，note4c 会把这条刻度画成斜纹。
  这里如果发 `0`，等于声称这个限额一点都没用过。
- **套餐里有单独计量的模型时会多出第三条刻度。** `rate_limits.model_scoped` 里是被单独计量的
  模型窗口——也就是 `/usage` 里的 "Fable this week"——它以自己的名字单独占一条，而不是被折进
  那个它本来就不属于的周限额里。
- **重置时间戳已经格式化好了**，因为只有插件知道服务端的本地时间。五小时窗口给的是时钟时间，
  一周窗口给的是日期——对于六天之后才重置的窗口，`@16:00` 什么也说明不了。

和 `display-projector` 一起启用前有一点需要知道。服务端把工具发出的数据包原样透传，不会补上
轮次编号，所以本插件用墙上时钟秒数给自己的卡片编号，以保证"刚刚问出来"的卡片一定能抢到客户端
那个 latest-wins 的槽位。客户端只保留同一会话里见过的最大序号，因此一旦显示过一张用量卡片，
projector 那些按对话自身的小计数器编号的卡片，在这次会话剩下的时间里就抢不过它了。

## 文件

| 文件 | 作用 |
| ---- | ---- |
| `plugin.yaml` | 清单。工具名、模型看到的描述、`enabled: false` |
| `index.ts` | 接线：initialize 时读配置，execute 时口播 + 发包 |
| `sources.ts` | 读取并归一化两个 agent 的文件 |
| `card.ts` | v1 卡片、刻度和口播文本 |
| `test.ts` | `npx tsx --test test.ts` |
