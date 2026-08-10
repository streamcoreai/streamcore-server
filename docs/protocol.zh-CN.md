[English](./protocol.md) | **简体中文**

# 协议参考

## WHIP 信令

信令遵循 [RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html)。

| 步骤 | 方法 | 路径 | 请求体 | 响应 |
|------|--------|------|------|----------|
| 1 | `POST` | `/whip` | SDP offer（`application/sdp`） | `201 Created`，返回 SDP answer、`Location: /whip/{sessionId}` 与 `ETag` |
| 2 | `DELETE` | `/whip/{sessionId}` | 无 | `200 OK` |
| — | `OPTIONS` | `/whip` 或 `/whip/{sessionId}` | 无 | `204 No Content`，带 `Accept-Post: application/sdp` |

`POST /whip` 按客户端 IP 限流（每分钟 30 个会话）。超出后服务返回 `429 Too Many Requests` 并带上 `Retry-After` 头。每次 POST 都会建立一个 peer connection 并收集 ICE，因此即使未开启鉴权，该端点也会被限流。

客户端创建 SDP offer、收集 ICE 候选并 `POST` 到 `/whip`。服务端创建 peer、收集自己的候选，并连同服务端生成的会话 ID 一起返回 answer。不使用 trickle ICE，也没有常驻信令连接。

本实现与 WHIP 的核心流程一致：以 `application/sdp` 发起 `POST`，用 `201 Created` 返回 answer，用 `Location` 给出会话 URL，用 `ETag` 标识 ICE 会话，用 `DELETE` 销毁，用 `OPTIONS` 返回 `Accept-Post`，并在双端做完整 ICE 收集。音频为 `sendrecv`，并带一个用于双向事件的 DataChannel。

## 实时事件

客户端必须在生成 offer 之前创建一个标签为 `events` 的 DataChannel。服务端会发送：

| 类型 | 载荷 | 说明 |
|------|---------|-------------|
| `transcript` | `{ "type": "transcript", "text": string, "final": boolean }` | 用户转写更新 |
| `response` | `{ "type": "response", "text": string }` | 流式回复文本 |
| `state` | `{ "type": "state", "state": "listening" \| "thinking" \| "speaking" }` | 智能体轮次状态，用于 UI 指示 |
| `timing` | `{ "type": "timing", "stage": string, "ms": number }` | `pipeline.debug = true` 时的时延数据 |

目前的 timing 阶段：`llm_first_token`、`tts_first_byte`。

客户端在同一通道上发送的消息会被路由进流水线 —— 目前用于 `vision.analyze` 插件消费的摄像头图像分片。

## 鉴权

设置 `server.jwt_secret` 后，`/whip` 会要求 `Authorization: Bearer <jwt>`。设置该项时，服务还会暴露 `POST /token`，签发有效期 1 小时的 HS256 token。再设置 `server.api_key`，则 `/token` 本身也要求 `Authorization: Bearer <api_key>`，这样只有你的后端才能签发会话 token。两者默认都为空，即关闭鉴权。
