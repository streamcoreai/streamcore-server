[English](./protocol.md) | **简体中文**

# 协议参考

## WHIP 信令

信令遵循 [RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html)。

| 步骤 | 方法 | 路径 | 请求体 | 响应 |
|------|--------|------|------|----------|
| 1 | `POST` | `/whip` | SDP offer（`application/sdp`） | `201 Created`，返回 SDP answer、`Location: /whip/{sessionId}`、`ETag` 与 `Accept-Patch` |
| 2 | `PATCH` | `/whip/{sessionId}` | ICE 重启片段（`application/trickle-ice-sdpfrag`） | `200 OK`，返回服务端片段与新的 `ETag` |
| 3 | `DELETE` | `/whip/{sessionId}` | 无 | `200 OK` |
| — | `POST` | `/whip?resume={token}` | SDP offer（`application/sdp`） | `201 Created`，重新挂接到该令牌对应的会话 —— StreamCore 扩展，见[会话恢复](#会话恢复session-resume) |
| — | `OPTIONS` | `/whip` 或 `/whip/{sessionId}` | 无 | `204 No Content`，带 `Accept-Post: application/sdp` 与 `Accept-Patch: application/trickle-ice-sdpfrag` |

`POST /whip` 按客户端 IP 限流（每分钟 30 个会话）；`PATCH` 使用独立计数（每分钟 60 次重启），因此网络频繁抖动的客户端不会消耗掉自己新建会话的额度。超出任一限制后服务返回 `429 Too Many Requests` 并带上 `Retry-After` 头。两者都会建立或重新收集 ICE，因此即使未开启鉴权也会被限流。

客户端创建 SDP offer、收集 ICE 候选并 `POST` 到 `/whip`。服务端创建 peer、收集自己的候选，并连同服务端生成的会话 ID 一起返回 answer。不使用 trickle ICE，也没有常驻信令连接。

本实现与 WHIP 的核心流程一致：以 `application/sdp` 发起 `POST`，用 `201 Created` 返回 answer，用 `Location` 给出会话 URL，用 `ETag` 标识 ICE 会话，用 `PATCH` 做 ICE 重启，用 `DELETE` 销毁，用 `OPTIONS` 返回 `Accept-Post`，并在双端做完整 ICE 收集。音频为 `sendrecv`，并带一个用于双向事件的 DataChannel。

### ICE 重启

短暂的网络事件 —— 手机在 Wi-Fi 与蜂窝之间切换、笔记本更换网络、空闲后 NAT 重新绑定 —— 会中断连通性，但通话本身并未结束。若用重新 `POST` offer 的方式恢复，会分配新的会话、新的流水线和新的 LLM 客户端，对话历史与滚动摘要随之丢失，开场白也会重播。`PATCH` 恢复的是*同一条*连接：ICE 凭据与候选是新的，但 `PeerConnection`、DTLS 关联、媒体轨道以及正在运行的流水线都保持不变。

客户端重新收集 ICE（浏览器中为 `pc.restartIce()`），并把得到的凭据与候选发送到 `Location` 头给出的会话 URL：

```http
PATCH /whip/{sessionId} HTTP/1.1
Content-Type: application/trickle-ice-sdpfrag
If-Match: "<etag>"

a=ice-ufrag:ZjRk
a=ice-pwd:AYk4ZQlPQeZ1AyJEkxUFXA
m=audio 9 UDP/TLS/RTP/SAVPF 111
a=mid:0
a=candidate:1 1 udp 2130706431 198.51.100.7 51000 typ host
```

服务端返回 `200 OK`，带上自己的片段与轮换后的 `ETag`：

```http
HTTP/1.1 200 OK
Content-Type: application/trickle-ice-sdpfrag
ETag: "<new-etag>"

a=ice-ufrag:MInq
a=ice-pwd:9NAlFOwsD1owEQGZjnjqvSVU
m=audio 9 UDP/TLS/RTP/SAVPF 111
a=mid:0
a=candidate:1 1 udp 2130706431 198.51.100.1 39132 typ host
a=end-of-candidates
```

`If-Match` 可选，但只要携带就会校验：其值必须是当前 `ETag`（或 `*`），否则服务端返回 `412 Precondition Failed`，并在响应中给出当前的 tag。其他情况：

| 状态码 | 含义 |
|--------|------|
| `404 Not Found` | 会话已被回收或从未存在 —— 用 `POST` 重新拨号。 |
| `405 Method Not Allowed` | 片段只有候选、没有 `ice-ufrag`/`ice-pwd`。按 RFC 9725 §4.4.1，trickle ICE 是可选的且本服务未实现；请收集完整后再 PATCH。 |
| `409 Conflict` | 会话存在，但没有已协商的 peer 可供重启。 |
| `415 Unsupported Media Type` | `Content-Type` 不是 `application/trickle-ice-sdpfrag`。 |

`disconnected` 连接状态是暂时的，本身绝不会拆毁 peer —— ICE 可以自行恢复；若无法恢复，Pion 会在约 25 秒后升级为 `failed`（该状态才会拆毁）。处于 disconnected 期间，服务端会在 DataChannel 上发出 `connection` 事件，客户端可据此提示“正在重连…”，恢复后再发一次。

### 恢复阶梯

上面两种机制并非二选一，而是有先后顺序的；除 Python 外的每个 SDK 都会依次执行：

| 时机 | 机制 | 保留下来的东西 |
|------|------|----------------|
| 连接处于 `disconnected` | **ICE 重启**（`PATCH`） | 全部。同一个 `PeerConnection`、同一个 DTLS、同样的媒体轨道 —— 传输层之上毫无察觉。 |
| 连接已 `failed` | **会话恢复**（`POST ?resume=`） | 对话本身。传输被彻底重建，但历史、滚动摘要与智能体的记忆都会延续。 |
| 令牌过期或会话已被回收 | 无 | 通话可用，但对话是空白的。服务端会通过 `X-Resume-Status: expired` 告知客户端。 |

两个阶段共用同一个截止时间。`disconnected` 大约 25 秒后升级为 `failed`，此后服务端还会把这段被遗弃的对话保留 `server.session_grace_ms`（默认 30 秒）才回收。花在重启上的时间就是不能用于重拨的时间，因此把 `reconnectAttempts` 设得很大，留给 `resumeAttempts` 的预算就更少。

为什么两者都要，而不是只用会话恢复？因为 ICE 重启是无感的：没有新的 DTLS 握手，没有轨道更替，除了 ICE 重新探测之外也没有空档。而恢复式重拨要付出一次完整的重新协商与短暂静音。只要 ICE 重启可用，它总是更好的选择；而在它不可用时，会话恢复是唯一还能奏效的手段。

### 会话恢复（Session resume）

**这是 StreamCore 的扩展，并非 RFC 9725 的一部分。**

ICE 重启能恢复「坏掉」的传输，但无法恢复「已经消失」的传输：超过约 25 秒后连接进入 `failed`，服务端已关闭该 peer，也就没有什么可重启的了。被切到后台、被挂起或断网一分钟的客户端正属于这种情况；任何 WebRTC 栈本身就无法执行 ICE 重启的客户端也是如此 —— Python SDK 就是其中之一。

会话恢复正是为这种情况准备的：允许一次全新的 `POST` 重新挂接到那条已断连接原本进行中的对话。传输是新的，但会话、带有消息历史的 LLM 客户端、转写记录与滚动摘要都不是新的，开场白也不会重播。

每次 `POST` 的响应都会带上一个令牌和一个状态：

```http
HTTP/1.1 201 Created
Location: /whip/{sessionId}
X-Resume-Status: new
X-Resume-Token: qcH8tnK-zT8...
```

恢复时，把该令牌作为查询参数重新拨号：

```http
POST /whip?resume=qcH8tnK-zT8... HTTP/1.1
Content-Type: application/sdp
```

| `X-Resume-Status` | 含义 |
|-------------------|------|
| `new` | 未携带令牌，是一次全新的对话。 |
| `resumed` | 已重新挂接。`Location` 仍是原会话 URL，智能体记得这通电话。 |
| `expired` | 令牌未知、已被使用，或其会话已被回收。**通话可用，但对话是空白的** —— 智能体不记得此前的内容。 |

携带过期令牌的重拨绝不会被直接拒绝，因为让整通电话失败比丢失历史更糟。状态头正是客户端用来区分二者的依据；悄无声息地从头开始，恰恰是这个令牌要防止的那种「失忆」。

有两条性质可以依赖：

- **令牌一次性使用。** 每次响应都会签发新令牌并让上一个失效，因此从日志或代理中截获的令牌，在合法客户端重拨的那一刻就已作废。
- **令牌不是会话 ID。** 会话 ID 会出现在资源 URL 和日志里；而一个能访问进行中对话的凭据不应如此，所以它是取自 `crypto/rand` 的 32 字节。

时间窗口为 `server.session_grace_ms`（默认 30 秒），从最后一个 peer 离开时开始计算。调大它可以给网络不稳定的客户端更多时间，代价是被遗弃的对话会在内存中保留更久。

实时（语音到语音）会话不提供恢复：它们的历史保存在服务商那一侧，新的服务商连接无法继承，因此宁可不签发令牌，也不承诺服务端无法兑现的连续性。

### 会话生命周期

会话会在客户端发送 `DELETE` 时移除，或在没有已连接 peer 的时间超过 `server.session_grace_ms`（默认 30 秒）后被回收。这段宽限期正是为 ICE 重启或重新拨号留出的窗口；而定期清扫则确保通话中途消失、因而根本无法发送 `DELETE` 的客户端，不会让会话泄漏到进程结束。

## 实时事件

客户端必须在生成 offer 之前创建一个标签为 `events` 的 DataChannel。服务端会发送：

| 类型 | 载荷 | 说明 |
|------|---------|-------------|
| `transcript` | `{ "type": "transcript", "text": string, "final": boolean }` | 用户转写更新 |
| `response` | `{ "type": "response", "text": string }` | 流式回复文本 |
| `state` | `{ "type": "state", "state": "listening" \| "thinking" \| "speaking" }` | 智能体轮次状态，用于 UI 指示 |
| `timing` | `{ "type": "timing", "stage": string, "ms": number }` | `pipeline.debug = true` 时的时延数据 |
| `connection` | `{ "type": "connection", "state": "reconnecting" \| "connected" }` | 传输已断开并正在恢复，或已恢复 |

目前的 timing 阶段：`llm_first_token`、`tts_first_byte`。

客户端在同一通道上发送的消息会被路由进流水线 —— 目前用于 `vision.analyze` 插件消费的摄像头图像分片。

### 显示卡片

`display.card` 是一个小而带版本号的语义载荷，面向墨水屏这类刷新很慢的常驻显示设备。它是附加能力，默认关闭。不认识它的客户端应当直接忽略，就像 NOTE4C 在做常驻渲染时会忽略实时的 `transcript`、`response` 和 `state` 事件一样。

```json
{
  "type": "display.card",
  "version": 1,
  "session_id": "9d2f...",
  "turn_id": "turn_123",
  "turn_seq": 123,
  "card": {
    "layout": "hero",
    "title": "Auckland · Tomorrow",
    "primary": "17°C",
    "secondary": "Rain after lunch",
    "detail": "Mostly cloudy · light winds"
  }
}
```

**它在链路上是被包了一层的。** 卡片属于插件发出的数据包，而所有按 topic 寻址的数据包到达客户端时都裹在一个 data 包里，payload 还做了 base64：

```json
{ "type": "data", "topic": "display.card", "payload": "<上面那个对象的 base64>" }
```

因此客户端应当从**按 topic 寻址的数据回调**里读卡片，而不是从原始事件流里读——原始事件流拿到的是外层信封，它的 `type` 是 `data`。把信封当成卡片去解析的客户端只会看到类型不对，然后把收到的每一张卡片都丢掉。ESP32 SDK 会替你拆掉这层信封，把解码后的字节交给 `on_data(topic, payload)`；`on_raw_event` 拿到的是信封本身。`transcript`、`response`、`state` 是以裸事件形式下发的，而 `display.card` 目前没有任何地方以裸事件形式发送——你可以同时兼容裸事件，但必须接受被包裹的形式，否则什么也收不到。

`turn_seq` 是在同一个 `session_id` 内递增的正整数，`turn_id` 是它对应的可读形式。屏幕很慢的设备应当只保留最新的有效卡片，而不是排成先进先出队列。当一张卡片的 `turn_seq` 不比同一会话里已接受的最新卡片更大时，应当拒绝它。`session_id` 变化即开启一个新序列。

服务端在发送前会校验并截断所有字段：

| 字段 | 适用布局 | 上限 |
|---|---|---|
| `title` | 全部 | 32 字符 |
| `primary` | `hero`、`status` | 24 字符 |
| `secondary` | `hero`、`status` | 48 字符 |
| `detail` | `hero` | 80 字符 |
| `body` | `text` | 180 字符 |
| `items` | `list` | 4 条 |
| 每条 `items[]` | `list` | 40 字符 |
| `columns` | `split` | 2 列 |
| `columns[].heading` | `split` | 17 字符 |
| `columns[].lines` | `split` | 7 行 |
| 每条 `columns[].lines[]` | `split` | 17 字符 |
| `weather.forecast` | `weather` | 3 天 |
| `weather.note` | `weather` | 34 字符 |
| 每个温度值 | `weather` | 4 字符 |
| 每条 `forecast[].label` | `weather` | 4 字符 |
| `agents` | `usage` | 3 个 |
| `agents[].name` | `usage` | 18 字符 |
| `agents[].note` | `usage` | 14 字符 |
| `agents[].gauges` | `usage` | 3 条 |
| `gauges[].label` | `usage` | 3 字符 |
| `gauges[].reset` | `usage` | 8 字符 |

布局语义：

- **hero** —— 一个占主导地位的事实（`title`，必填 `primary`，可选 `secondary` / `detail`）。
- **text** —— 一段紧凑的说明（`title`，必填 `body`）。
- **list** —— 最多四条短条目（`title`，必填且非空的 `items`）。
- **status** —— 一个已完成的动作或确认（`title`，必填 `primary`，可选 `secondary`）。
- **weather** —— 一份天气（`title`，必填 `primary` 和 `weather`）。见下文。
- **usage** —— 一个主体一块用量面板（`title`，必填且非空的 `agents`）。见下文。
- **split** —— 并排放置的两样东西（`title`，必填且非空的 `columns`）。每一列包含一个 `heading` 和若干短 `lines`；空字符串是调用方要求的占位行，客户端为它留一行但不画内容。用于把一样东西和另一样对比着看，这是任何单列布局都表达不了的。

```json
{
  "layout": "split",
  "title": "AI Usage",
  "columns": [
    { "heading": "Claude 2m", "lines": ["5H 62% used", "======----", "38% left 2h 10m"] },
    { "heading": "Codex 5m", "lines": ["5H 16% used", "==--------", "84% left 4h"] }
  ]
}
```

`split` 是给自己发卡片的插件用的；display projector 永远不会产出这种布局，因为被投影的一轮对话只有一个主题，不是两个。早于这个布局的客户端会把卡片当作未知布局拒绝掉，屏幕上保持原样。

#### weather

有两类内容在常驻屏幕上出现得足够频繁，值得拥有自己的结构化布局，天气是其中之一。卡片里依然没有像素：它只说明这是什么天气，具体画成什么图标由客户端决定。

```json
{
  "layout": "weather",
  "title": "Auckland",
  "primary": "17",
  "secondary": "Partly cloudy",
  "detail": "Saturday 18 May",
  "weather": {
    "icon": "partly",
    "unit": "C",
    "high": "19",
    "low": "11",
    "note": "Take an umbrella Monday",
    "forecast": [
      { "label": "SUN", "icon": "rain", "high": "18", "low": "10" },
      { "label": "MON", "icon": "sun", "high": "21", "low": "12" }
    ]
  }
}
```

`title` 是地点，`primary` 是当前温度且只有数字，`secondary` 是天气状况的文字描述，`detail` 是这份数据对应的日期。温度里不带度数符号，也不带单位——单位由 `unit` 用 `C` 或 `F` 说明一次，其余部分由客户端自己画：把非 ASCII 字符一律换成 `?` 的面板，渲染不了夹在字符串里的 `°`。

`icon` 取值为 `sun`、`moon`、`partly`、`cloud`、`rain`、`storm`、`snow`、`fog`、`wind` 之一。**客户端不得因为不认识某个图标名就拒绝整张卡片**——画一个中性图标、保留温度即可，那仍然是用户要的答案。`note` 是卡片底部的一行提示，可选；`forecast` 可选，最多三天。

#### usage

另一类：某样按额度计量的东西用掉了多少。一个主体一块面板，一个窗口一条刻度。

```json
{
  "layout": "usage",
  "title": "AI Usage",
  "agents": [
    {
      "name": "Claude Code",
      "note": "as of 06:11",
      "gauges": [
        { "label": "5H", "percent": 62, "reset": "@14:10" },
        { "label": "7D", "percent": 17, "reset": "@Sep 7" }
      ]
    },
    { "name": "Codex", "note": "no recent runs", "gauges": [] }
  ]
}
```

`percent` 是**已用**百分比：0 到 100 的整数，数据源没给出时为 `null`——客户端必须把它画得和 0 不一样，因为"没读到"和"没用过"不是同一个事实。

之所以传"已用"，是因为数据源报出来的原始事实就是这个。客户端完全可以改画"还剩多少"——NOTE4C 就是这么做的，因为对着一块挂在墙上的屏幕，你问的是"我还剩多少"——但不管选哪一种，**都必须在画面上写清楚是哪一种**。这两个工具自己就不统一：Claude Code 的 `/usage` 写的是 "26% used"，Codex CLI 写的是 "100% remaining"。一个光秃秃的百分比，读的人只能猜它的方向，而且有一半的人会猜反。超出范围的值会被夹到区间内，而不是导致整张卡片被拒。`reset` 由服务端格式化好，因为只有服务端知道自己的本地时间。`gauges` 允许为空：没有数据可报的 agent 仍然保留自己的面板，并在 `note` 里说明原因。

这两种布局都是给自己发卡片的插件用的。display projector 两种都不会产出，因为它只能看到一轮对话的文本，那样等于凭空编造结构。

载荷里只有语义。StreamCore 不下发坐标、字体、颜色、帧缓冲、PNG，也不下发任何设备相关的渲染指令。排版、换行、配色，以及什么时候值得花一次刷新，都由客户端自己决定。推荐的设备策略是：只存最新的一张，等到助手播放结束、并且确认用户没有接着说话之后，再消抖 1–3 秒，然后花掉一次刷新。

## 鉴权

设置 `server.jwt_secret` 后，`/whip` 会要求 `Authorization: Bearer <jwt>`。设置该项时，服务还会暴露 `POST /token`，签发有效期 1 小时的 HS256 token。再设置 `server.api_key`，则 `/token` 本身也要求 `Authorization: Bearer <api_key>`，这样只有你的后端才能签发会话 token。两者默认都为空，即关闭鉴权。

### 通话方身份

`POST /token` 接受一个可选的请求体，用于标明这个 token 属于谁：

```json
{ "resource_id": "user_8891" }
```

该值会作为 `sub` claim 签进 token，`/whip` 再以 `resource_id` 字段转发给外部 agent（参见[自带 agent](./bring-your-own-agent.zh-CN.md)）。`session_id` 划定的是一次对话，而它划定的是跨越所有对话的那个人——正是它让 agent 能认出昨天挂断、今天又打回来的来电者。

请在这里签发，而不要让客户端自行上报：`/token` 由你的后端调用，它持有 API key，本来就知道当前登录的是哪个用户；而 `/whip` 是由浏览器调用的。页面无法伪造一个它签不出来的 claim。

没有 token 端点、直接拨 `/whip` 的服务端客户端可以改为发送：

```http
X-StreamCore-Resource-Id: +14155550123
```

该请求头**仅在**请求不带已签名 claim 时才会被采纳，因此永远无法覆盖 claim。它同时也不在 CORS 的 `Access-Control-Allow-Headers` 列表中，这意味着浏览器根本发不出这个头——它是留给受信任的服务端调用方的，例如 [sip-server](../../sip-server/README.zh-CN.md)，它会把来电号码填进去。

身份始终是可选的。不提供身份的部署只会在 agent 请求中省略 `resource_id`，agent 退回到按会话划定记忆。
