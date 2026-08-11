[English](./capabilities.md) | **简体中文**

# 能力清单

媒体运行时**今天**具备的能力。不在本页的内容都在[路线图](./roadmap.zh-CN.md)里，尚未进入产品。

可以用 StreamCore 构建：

- 语音智能体
- 实时副驾
- 同声传译
- AI 主持的音频体验
- 嵌入式语音设备
- 电话与通信应用
- 定制的实时 AI 产品

## StreamCore 解决的问题

做一个智能体 demo 很容易，围绕它搭出可靠的实时媒体基础设施则不然。

当原型要变成产品时，难点都不在提示词上：

| 问题 | StreamCore 今天做了什么 |
|---------|----------------------------|
| WebRTC 连接 | 基于 Pion 的 peer、完整 ICE 收集、通过一次 HTTP POST 完成的 WHIP 信令 |
| NAT 穿透 | 内置 STUN/TURN 服务 —— 无需额外的 coturn 容器 |
| 音频传输 | 双向 Opus over RTP，编解码由框架处理 |
| 轮次控制 | 自适应 VAD 跟踪每通电话的噪声基线，并用去抖把说话人句中的停顿合并为同一轮 |
| 插话打断 | 使用更快 VAD 配置的 barge-in：先压低智能体音量，过滤掉 "嗯嗯"、"好的" 这类回应词，确认打断后取消进行中的 LLM 与 TTS |
| 流式服务商集成 | 流式 STT、流式 LLM 与分块流式 TTS，合成尚未结束音频就已开始播放 —— 端到端打通 |
| 会话状态 | 服务端生成的会话 ID、多 peer 会话、生命周期与销毁 |
| 实时事件 | DataChannel 事件：转写、回复、智能体状态与时延数据 |
| 客户端集成 | TypeScript、React Native、Python、Go、Rust SDK |
| 电话 | SIP 网桥组件，在 PCMU ↔ Opus 之间转码并通过 WHIP 接入 |
| 鉴权 | `/whip` 上可选的 JWT 鉴权，配合短期 token 端点 |
| 过载与故障隔离 | 按 IP 限流加全局 `max_sessions` 上限（503 带 `Retry-After`），以及按通话隔离的 panic 恢复 —— 一个异常通话不再拖垮整个进程 |
| 密钥管理 | 所有 API key 与密钥都可用环境变量注入，密钥不进入镜像与配置文件 |
| 时延可见性 | DataChannel 时延事件，以及日志中每轮的时延分解（端点检测、合并、embedding、向量检索、LLM、TTS） |

## 它的位置

```text
┌───────────────────────────────────────────────┐
│              Applications                     │
│ Voice agents · Copilots · Translation · Rooms │
└───────────────────────┬───────────────────────┘
                        │ SDKs and realtime events
┌───────────────────────▼───────────────────────┐
│           StreamCore Media Runtime            │
│                                               │
│ WebRTC · RTP · Opus · Sessions · Interruption │
│ VAD · Streaming audio · Network traversal     │
└───────────────┬───────────────────┬───────────┘
                │                   │
       ┌────────▼────────┐  ┌───────▼──────────┐
       │ AI and speech   │  │ Application and  │
       │ services        │  │ agent backends   │
       │ STT · TTS · LLM │  │ Tools · APIs     │
       └─────────────────┘  └──────────────────┘
```

StreamCore 可以跑通一条完整的「语音 → 智能体 → 语音」链路，但那只是它的用法之一。

## 实时媒体能力

**传输与连接**

- 基于 WebRTC 的双向 Opus 音频（`sendrecv`）
- WHIP 信令（[RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html)）—— 一次 HTTP `POST` 完成 SDP 交换，无需常驻信令连接
- 双端完整 ICE 收集，不使用 trickle ICE
- 通过 `PATCH /whip/{sessionId}` 支持 ICE 重启 —— 网络切换或 NAT 重新绑定可在同一条连接上恢复，对话、流水线与 LLM 客户端均得以保留
- 空闲会话在可配置的宽限期后被回收，既能收走通话中途消失的客户端，又不会打断正在重连的客户端
- 对于 ICE 重启已无能为力的断线，提供会话恢复：携带一次性令牌重拨即可重新挂接到进行中的对话。各 SDK 会把两者作为一个阶梯依次使用（还能救则重启，救不回来则重拨），因此被切到后台的手机或休眠过的笔记本都会重新回到同一段对话，而不是另起一段
- 基于 Pion 的内置 STUN/TURN 服务（UDP **与 TCP** 3478，中转端口 50001–60000）—— 支持 TCP，因此处在封禁 UDP 的防火墙后的用户仍能接通
- `/whip` 上可选的 JWT 鉴权，`POST /token` 签发有效期 1 小时的 token
- `/health` 端点，以及带强制退出兜底的优雅关闭

**媒体链路**

- Opus 解码 → PCM → 处理流水线 → PCM → Opus 编码 → RTP
- 基于能量的 VAD，起止帧数可配置，并自适应每通电话的噪声基线，因此安静线路上的轻声用户与路边嘈杂环境中的用户都能被正确识别
- 使用更快 VAD 配置的 barge-in：用户抢话时智能体音量随即压低，若判定只是回应词则恢复
- 轮次去抖，把连续的 final 转写合并，让「我想…嗯…订个位」只被回答一次，而不是两次
- 按句边界分块，使 TTS 在 LLM 生成结束前就开始；再按 chunk 级流式播放，使一句话尚未合成完就已出声
- 可选的逐句表达标签 —— 模型可以在句首加上 `[warm]`、`[empathetic]`、`[calm]` 或 `[excited]`，它们会映射到服务商的音色控制参数，并且永远不会被读出来
- 思考音 —— 在慢速工具运行期间通过 RTP 播放的可选提示音（500 毫秒宽限期）

**会话与事件**

- 服务端生成的会话 ID，内存态会话管理器
- 单个会话可包含多个 peer，每个 peer 有入向或出向方向
- DataChannel 上的 `events` 通道，推送 `transcript`、`response`、`state` 与 `timing`
- 当 `pipeline.debug = true` 时记录每轮时延分解，区分端点检测、轮次合并、embedding、向量检索、LLM 与 TTS
- 客户端发来的 DataChannel 消息会被路由进流水线（目前用于摄像头图像分片）

**客户端**

- TypeScript（`@streamcore/js-sdk`）、Python（`streamcore`）、Go（`github.com/streamcoreai/go-sdk`）、Rust
- React Native / Expo（`@streamcore/react-native-sdk`）—— 已完成，尚未发布到 npm

## 支持的端点

| 端点类型 | 状态 | 方式 |
|---------------|--------|-----|
| 浏览器 | 可用 | TypeScript SDK over WHIP |
| 移动端 | 可用 | React Native / Expo SDK（peer 依赖 `react-native-webrtc`） |
| 后端服务 / worker | 可用 | Go、Python 或 Rust SDK |
| CLI 与 TUI | 可用 | Go 与 Rust 示例 |
| 电话（SIP） | 可用 | [`sip-server`](https://github.com/streamcoreai/sip-server) 在 PCMU/RTP ↔ Opus/WHIP 之间转换，支持呼入与呼出 |
| 嵌入式设备 | 实验性 | [`esp32`](https://github.com/streamcoreai/esp32) 中的 ESP32-S3 固件直接使用 WHIP |

## AI 集成

| AI 集成 | 服务商 |
|----------------|-----------|
| 流式 STT | Deepgram、AssemblyAI、OpenAI、VibeVoice（本地） |
| LLM | OpenAI、Ollama（本地或自托管），或你自己的 HTTP 智能体端点（`agent`） |
| 流式 TTS | Cartesia、Deepgram、ElevenLabs、MiniMax、Speechify、VibeVoice（本地） |
| 语音到语音 | xAI Grok Voice（用一个模型取代 STT + LLM + TTS） |
| 检索 | pgvector、Supabase |
| 自定义工具 | Python / TypeScript / JavaScript 插件，原生 Go 工具 |

凭据与各服务商的注意事项见[服务商](./providers.zh-CN.md)。
