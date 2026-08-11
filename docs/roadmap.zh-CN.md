[English](./roadmap.md) | **简体中文**

# 路线图 / TODO

本页**未打勾**的内容都是**尚未实现**的。把它们放在这里，是为了让[能力清单](./capabilities.zh-CN.md)保持诚实 —— 只要在本页仍未打勾，就不要按它来做今天的规划。

已打勾的条目表示已经交付。它们会再保留一两个版本，以便本页体现出进展，并各自链接到已实现功能的文档。

需要其中某一项？在 [Discord](https://discord.gg/xKGFaGWawT) 说一声或提一个 issue —— 需求会改变这份清单的顺序。欢迎贡献代码，见 [CONTRIBUTING.md](../CONTRIBUTING.md)。

## 生产环境加固

- [x] **会话重连（服务端）。** 断开的连接现在可以通过 ICE restart（`PATCH /whip/{sessionId}`）在同一会话上恢复，而不再意味着通话结束。`PeerConnection`、DTLS 关联、正在运行的流水线与 LLM 客户端全部保留，因此对话历史与滚动摘要也得以保留，开场白不会重播。客户端凭空消失后残留的会话，会在 `server.session_grace_ms` 之后被回收。见[协议参考 → ICE 重启](./protocol.zh-CN.md#ice-重启)。
- [x] **客户端侧重连。** TypeScript、React Native、Go 与 Rust SDK 会自行发现断开并完成恢复，宿主应用无需介入：先在约 25 秒窗口内以退避方式做三次 ICE 重启，若连接仍然失败，再做两次恢复式重拨。应用真正需要处理的只有 `recovered-without-history` 这一种结果 —— 通话可用，但智能体已经忘记了之前的对话。见[协议参考 → 恢复阶梯](./protocol.zh-CN.md#恢复阶梯)，可调参数见各 SDK 的 README。
- [x] **会话恢复。** 一旦连接进入 `failed`，ICE 重启就无能为力了 —— peer 已被关闭，没有什么可重启的。此时携带一次性令牌重拨即可重新挂接到进行中的对话，保留 LLM 历史、转写记录与滚动摘要，并跳过开场白。各 SDK 会把「先重启、后恢复」作为同一个阶梯执行；Python 只有恢复这一半，因为 aiortc 没有 ICE restart 原语（`createOffer()` 不接受参数，aioice 的凭据在构造时即固定），也没有可供触发的 `disconnected` 状态。见[协议参考 → 恢复阶梯](./protocol.zh-CN.md#恢复阶梯)。
- [ ] **Panic 恢复。** 任何一个会话的流水线 goroutine 发生 panic，都会连带整个进程与所有进行中的通话一起崩溃。每个会话的 goroutine 应当 recover、记录调用栈，并只拆除自己所在的会话。
- [ ] **会话上限。** 限流按客户端 IP 进行，没有全局的 `max_sessions`。每个会话都消耗 CPU 与服务商费用，分布式客户端仍可无限堆积会话。返回 503 加 `Retry-After` 的上限能约束影响范围。
- [ ] **环境变量注入密钥。** API key 目前只能写在 `config.toml` 里 —— 没有任何代码读取环境变量。容器与云端部署希望以环境变量注入密钥，避免密钥进入镜像与文件。
- [ ] **指标与可观测性。** 已有 `/health` 与 DataChannel 时延事件，但没有 Prometheus/OpenTelemetry 导出。
- [ ] **结构化日志。** 现在全部是 `log.Printf` 文本。生产环境需要每行携带 `session_id` 的 JSON 日志 —— 用 `log/slog` 配合会话级 logger，可以渐进迁移。
- [ ] **版本化发布。** CI 会构建与测试，但没有 release 工作流、二进制中没有嵌入版本号，也没有发布的 Docker 镜像或带 tag 的二进制 —— 每个用户都得从源码构建。

## 接入自有智能体

- [x] **HTTP 智能体端点。** 已随 `llm.provider = "agent"` 发布 —— webhook 形式的智能体后端，[接入自有智能体](./bring-your-own-agent.zh-CN.md)不再需要写 Go 代码。兼容 OpenAI 的 `base_url` 模式仍在计划中。
- [ ] **持久记忆。** 内置运行时在会话之间不记得来电者 —— 滚动摘要随通话开始、随通话结束。[自有智能体](./bring-your-own-agent.zh-CN.md)已经可以自行持久化记忆。

## 生态

- [ ] **更多示例**，以印证产品定位：实时翻译、AI 主持的语音房间、浏览器副驾、嵌入式设备、SIP 应用，以及一个完全不含 LLM 的纯音频处理应用。
- [ ] **嵌入式客户端加固。** [`esp32`](https://github.com/streamcoreai/esp32) 中的 ESP32-S3 固件已能通过 WHIP 连接，但尚未达到生产可用。
- [ ] **`streamcore-cli` 发布。** RAG 的文档入库文档所依赖的二进制程序尚未公开 —— 见[智能体运行时 → 文档入库](./agent-runtime.zh-CN.md#文档入库)。
- [ ] **React Native SDK 发布到 npm。** `@streamcore/react-native-sdk` 已完成、可从源码使用，但尚未发布。

---

已经交付并受支持的部分：[能力清单](./capabilities.zh-CN.md)。
