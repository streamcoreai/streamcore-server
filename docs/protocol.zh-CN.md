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

## 鉴权

设置 `server.jwt_secret` 后，`/whip` 会要求 `Authorization: Bearer <jwt>`。设置该项时，服务还会暴露 `POST /token`，签发有效期 1 小时的 HS256 token。再设置 `server.api_key`，则 `/token` 本身也要求 `Authorization: Bearer <api_key>`，这样只有你的后端才能签发会话 token。两者默认都为空，即关闭鉴权。
