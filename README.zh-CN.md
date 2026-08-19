<div align="center">

<img src="./assets/logo.png" alt="StreamCore" width="320" />

# StreamCore

### 面向 AI 应用的实时媒体基础设施

**通过 WebRTC 与你的 AI 对话 —— 插话打断、流式语音、NAT 穿透全部就绪。**<br/>
一个 Go 二进制，智能体依然归你。

### [▶ 立即在 streamcore.ai 对话](https://streamcore.ai)

无需安装、无需注册 —— 打开浏览器麦克风，你可以随时把它打断。

[![CI](https://github.com/streamcoreai/streamcore-server/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/streamcoreai/streamcore-server/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/streamcoreai/streamcore-server?logo=go&logoColor=white)](./go.mod)
[![WHIP RFC 9725](https://img.shields.io/badge/WHIP-RFC%209725-6f42c1)](https://www.rfc-editor.org/rfc/rfc9725.html)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](./LICENSE)
[![Stars](https://img.shields.io/github/stars/streamcoreai/streamcore-server?logo=github&color=f5c518)](https://github.com/streamcoreai/streamcore-server/stargazers)
[![Discord](https://img.shields.io/badge/join%20us%20on-discord-5865F2?logo=discord&logoColor=white)](https://discord.gg/xKGFaGWawT)
[![Follow @jasonshen_](https://img.shields.io/badge/follow-%40jasonshen__-000000?logo=x&logoColor=white)](https://x.com/jasonshen_)

[**在线演示**](https://streamcore.ai) · [**快速开始**](#快速开始) · [**文档**](./docs/README.zh-CN.md) · [**SDK**](#sdk-与示例) · [**路线图**](./docs/roadmap.zh-CN.md) · [**Discord**](https://discord.gg/xKGFaGWawT) · [English](./README.md)

</div>

---

做一个语音智能体 demo 谁都可以。可一旦真实用户开口打断它、说到一半停顿、从封禁 UDP 的公司网络拨进来，或者等了三秒还没听到第一个字 —— demo 就不再是产品了。

StreamCore 正是处理这一切的那一层。它掌管用户与你的 AI 之间对延迟敏感的媒体链路：**WebRTC 传输、自适应轮次控制、插话打断、流式 STT/LLM/TTS、NAT 穿透、会话状态与实时事件** —— 覆盖浏览器、手机、后端服务、电话与嵌入式设备。

它刻意不接管的，是你的智能体。提示词、工具、模型与业务逻辑都留在原处，[五种接入方式](#接入你自己的智能体)，无需 fork。

已经用它构建的：语音智能体、实时副驾、同声传译、AI 主持的语音房间、嵌入式语音设备与电话应用。

## 演示

**[streamcore.ai](https://streamcore.ai) 跑的就是这个仓库。** 打开页面点 *Start Conversation*，在它说话时直接插话打断，
屏幕上会实时显示每一轮的 STT、LLM、TTS 延迟。

<a href="https://streamcore.ai" target="_blank">
  <img src="https://cdn.loom.com/sessions/thumbnails/ee079aca75aa4fa1ba6a5e51302fbd56-e4ee3f1f1a14a51d.jpg" alt="在 streamcore.ai 与它对话" />
</a>

想先看录屏？[观看演示视频](https://www.loom.com/share/ee079aca75aa4fa1ba6a5e51302fbd56)。

## 快速开始

**两个终端、五分钟，你就能跟它说上话。** 需要 Go 1.25+（或 Docker），以及 STT、LLM、TTS 服务商的 API key。没有 key？可以用 Ollama + VibeVoice [完全本地运行](./docs/quickstart.zh-CN.md#完全本地运行无需-api-key)。

```bash
cp config.toml.example config.toml   # 填入你的服务商凭据
go run .
```

服务监听 `:8080`，客户端连接 `http://localhost:8080/whip`。

然后在浏览器里跟它对话：

```bash
git clone https://github.com/streamcoreai/examples.git
cd examples/typescript && npm install && npm run dev
```

打开 [http://localhost:3000](http://localhost:3000) 开始说话。

Docker、TURN 端口与生产部署注意事项见[快速开始指南](./docs/quickstart.zh-CN.md)。

## 你会得到什么

| | |
|---|---|
| **传输** | 基于 WHIP（[RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html)）的 WebRTC 音频 —— 一次 HTTP POST，无需常驻信令连接。双向 Opus/RTP |
| **连通性** | 内置 Pion STUN/TURN，同时监听 UDP 与 TCP 3478 —— 无需额外的 coturn。网络切换或 NAT 重新绑定可在同一会话上通过 ICE restart 恢复，对话不会因此中断 |
| **轮次控制** | 自适应 VAD 跟踪每通电话的噪声基线，并用去抖把句中停顿合并为同一轮 |
| **插话打断** | Barge-in 先压低智能体音量，过滤 "嗯嗯" 这类回应词，确认打断后取消进行中的 LLM 与 TTS |
| **流式链路** | 流式 STT → 流式 LLM → 分块流式 TTS，合成尚未结束音频就已开始播放 |
| **会话与事件** | 服务端生成会话 ID、多 peer 会话，DataChannel 推送转写、回复、状态与每轮延迟 |
| **接入范围** | 浏览器、移动端、后端服务、CLI、[SIP 电话](https://github.com/streamcoreai/sip-server) 与 [ESP32](https://github.com/streamcoreai/esp32) 设备 |

完整能力清单：[能力清单](./docs/capabilities.zh-CN.md)。

## 尚未实现

列在这里是为了让上面的表格保持诚实 —— 未打勾的是当前真实存在的空缺，而不是即将交付的承诺；已打勾的是近期已交付的，会再保留一两个版本，方便你看到进展：

- [x] **会话重连（服务端）** —— 断开的连接可通过 ICE restart 在同一会话上恢复，对话与正在运行的流水线都得以保留
- [x] **客户端侧重连** —— TypeScript、React Native、Go 与 Rust SDK 会在网络变化后自动恢复：先做 ICE 重启，若连接已失败则改用恢复式重拨
- [x] **会话恢复** —— 对于 ICE 重启已无能为力的断线，可携带一次性令牌重拨并重新挂接到进行中的对话。各 SDK 会把「先重启、后恢复」作为同一个阶梯执行，因此被切到后台的手机也能重新回到同一段对话
- [x] **Panic 恢复** —— 一个通话的 goroutine 发生 panic 现在只影响该通话：recover、记录调用栈，会话像正常结束的通话一样被回收
- [x] **会话上限** —— `server.max_sessions` 全局限制活跃会话数；超过后 `POST /whip` 返回 503 并带 `Retry-After`。会话恢复不受限制
- [x] **环境变量注入密钥** —— 所有 API key 与密钥都可以从环境变量注入（`OPENAI_API_KEY`、`STREAMCORE_JWT_SECRET` 等），不必写进 `config.toml`。见[配置参考](./docs/configuration.zh-CN.md#用环境变量注入密钥)
- [ ] **指标导出** —— 有 `/health` 与时延事件，但没有 Prometheus/OpenTelemetry
- [ ] **结构化日志** —— 现在是 `log.Printf` 文本，没有携带 `session_id` 的 JSON 日志
- [ ] **版本化发布** —— GitHub release 发布时会推送 Docker 镜像到 GHCR，但二进制中还没有版本号，也没有带 tag 的独立二进制
- [ ] **水平扩展** —— 会话存于进程内存，服务是单节点的；重连与会话恢复要在负载均衡器后工作，需要粘性路由或外部存储
- [x] **HTTP 智能体端点** —— `llm.provider = "agent"` 把每一轮对话 POST 到你用任意语言托管的智能体，回复以语音流式播报
- [ ] **持久记忆** —— 内置运行时在会话之间不记得来电者；自有智能体已经可以自行持久化记忆

完整 TODO 清单（含生态相关项）：[路线图 / TODO](./docs/roadmap.zh-CN.md)。想要其中某一项？在 [Discord](https://discord.gg/xKGFaGWawT) 说一声 —— 需求会改变优先级。

## 接入你自己的智能体

StreamCore 位于「提示词 + 工具」类框架的下一层：媒体链路。智能体始终归你，有五种方式 ——

1. **工具调用** —— 用插件（Python/TS/JS）或原生 Go 工具调用你已有的后端
2. **你的智能体** —— 设 `llm.provider = "agent"`，每一轮对话都会 POST 到你用任意语言托管的 HTTP 端点
3. **你的模型端点** —— 把 `llm.provider = "ollama"` 指向任何兼容 Ollama 的地址
4. **你的代码** —— 实现一个很小的 Go 接口，整条媒体链路无需改动
5. **内置运行时** —— 或直接使用 StreamCore 可选的智能体运行时（工具、技能、RAG、对话历史）

详情与代码：[接入你自己的智能体](./docs/bring-your-own-agent.zh-CN.md) · [智能体运行时](./docs/agent-runtime.zh-CN.md)。

服务商：Deepgram、AssemblyAI、OpenAI、Cartesia、ElevenLabs、MiniMax、Speechify、Ollama、VibeVoice（本地）、xAI Grok Voice（语音到语音），检索支持 pgvector / Supabase。见[服务商](./docs/providers.zh-CN.md)。

## 文档

| 页面 | 内容 |
|------|--------------|
| [快速开始](./docs/quickstart.zh-CN.md) | Docker、TURN 端口、接入客户端、接入自有后端、完全本地运行 |
| [能力清单](./docs/capabilities.zh-CN.md) | 当前具备的能力、支持的端点、AI 集成 |
| [接入你自己的智能体](./docs/bring-your-own-agent.zh-CN.md) | 掌控智能体的五种方式，含 HTTP 智能体端点与 `llm.Client` 接口 |
| [智能体运行时](./docs/agent-runtime.zh-CN.md) | 插件、技能、RAG、文档入库 |
| [服务商](./docs/providers.zh-CN.md) | Grok 语音到语音、MiniMax、本地 VibeVoice 及各服务商注意事项 |
| [配置](./docs/configuration.zh-CN.md) | 完整带注释的 `config.toml` 参考 |
| [协议](./docs/protocol.zh-CN.md) | WHIP 信令、DataChannel 事件、鉴权 |
| [架构](./docs/architecture.zh-CN.md) | 媒体流转、为什么用 Go、包结构 |
| [路线图 / TODO](./docs/roadmap.zh-CN.md) | **尚未实现**部分的清单 |

英文版文档见 [docs/](./docs/)。

## SDK 与示例

从任何地方接入 —— 所有 SDK 使用同一套 WHIP + DataChannel 协议：

[![npm](https://img.shields.io/npm/v/@streamcore/js-sdk?logo=npm&logoColor=white&label=%40streamcore%2Fjs-sdk)](https://github.com/streamcoreai/js-sdk)
[![PyPI](https://img.shields.io/pypi/v/streamcore?logo=pypi&logoColor=white&label=streamcore)](https://github.com/streamcoreai/python-sdk)
[![Go](https://pkg.go.dev/badge/github.com/streamcoreai/go-sdk.svg)](https://pkg.go.dev/github.com/streamcoreai/go-sdk)
[![crates.io](https://img.shields.io/crates/v/streamcore-rust-sdk?logo=rust&logoColor=white&label=streamcore-rust-sdk)](https://github.com/streamcoreai/rust-sdk)

React Native / Expo（`@streamcore/react-native-sdk`）已完成，尚未发布到 npm。

插件 SDK：[plugin-sdk](https://github.com/streamcoreai/plugin-sdk) 中的 `@streamcore/plugin` 与 `streamcore-plugin`。可直接运行的浏览器、CLI、TUI 示例：[examples](https://github.com/streamcoreai/examples)。

## 赞助与支持

<!-- The public live demo is powered by generous API credits from our sponsors. -->

<div align="center">
<!-- Logos will go here once received -->
</div>

感谢！有意赞助？欢迎联系，可在 GitHub 与演示页展示 logo。

## 参与贡献

请先阅读 [CONTRIBUTING.md](./CONTRIBUTING.md) —— 其中说明了如何本地运行服务、CI 在推送前会跑的四项检查，以及改动时延敏感的媒体链路时需要额外注意什么。适合上手的地方：[`good first issue`](https://github.com/streamcoreai/streamcore-server/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22) 与 [`help wanted`](https://github.com/streamcoreai/streamcore-server/issues?q=is%3Aissue+is%3Aopen+label%3A%22help+wanted%22)。

客户端 SDK、SIP 网桥、示例与 ESP32 固件都在 [`streamcoreai`](https://github.com/streamcoreai) 下各自的仓库中 —— 相关改动请提交到那里。

## 安全

发现漏洞？请不要公开开 issue，通过 [Security 页面](https://github.com/streamcoreai/streamcore-server/security/advisories/new)私下报告。[SECURITY.md](./SECURITY.md) 说明了范围、响应时限，以及部署在公网地址时最关键的设置 —— 首要是 `/whip` 的 JWT 鉴权。

## Star 历史

<!--
  Live chart from star-history.com. GitHub restricted the stargazers API to repo
  admins/collaborators on 2026-06-30, so the chart renders only when a sealed
  (encrypted) GitHub token is supplied. If it ever shows "GitHub restricted access
  to star data", that token has expired or been revoked — regenerate it at
  https://star-history.com/#streamcoreai/streamcore-server&Date under "Show
  real-time chart on your README.md" and replace sealed_token in all three URLs.
-->
<a href="https://www.star-history.com/?type=date&repos=streamcoreai%2Fstreamcore-server">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=streamcoreai/streamcore-server&type=date&theme=dark&legend=top-left&sealed_token=Fe6rTffC730520Ua9jYN4AQoEmFMNIwEPzp19cmksSRM4GuvuYib6iu6TxRTv0k51n0-B9kO6FI-N9-pJH6WB8XGn4GH-gKnIz-ou7n3ctqiKQ3IO9LuBg" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=streamcoreai/streamcore-server&type=date&legend=top-left&sealed_token=Fe6rTffC730520Ua9jYN4AQoEmFMNIwEPzp19cmksSRM4GuvuYib6iu6TxRTv0k51n0-B9kO6FI-N9-pJH6WB8XGn4GH-gKnIz-ou7n3ctqiKQ3IO9LuBg" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=streamcoreai/streamcore-server&type=date&legend=top-left&sealed_token=Fe6rTffC730520Ua9jYN4AQoEmFMNIwEPzp19cmksSRM4GuvuYib6iu6TxRTv0k51n0-B9kO6FI-N9-pJH6WB8XGn4GH-gKnIz-ou7n3ctqiKQ3IO9LuBg" />
 </picture>
</a>

## 许可证

Apache 2.0，详见 [LICENSE](./LICENSE)。
