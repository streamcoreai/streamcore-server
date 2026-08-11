[English](./roadmap.md) | **简体中文**

# 路线图 / TODO

本页**未打勾**的内容都是**尚未实现**的。把它们放在这里，是为了让[能力清单](./capabilities.zh-CN.md)保持诚实 —— 只要在本页仍未打勾，就不要按它来做今天的规划。

已打勾的条目表示已经交付。它们会再保留一两个版本，以便本页体现出进展，并各自链接到已实现功能的文档。

需要其中某一项？在 [Discord](https://discord.gg/xKGFaGWawT) 说一声或提一个 issue —— 需求会改变这份清单的顺序。欢迎贡献代码，见 [CONTRIBUTING.md](../CONTRIBUTING.md)。

## 生产环境加固

- [ ] **水平扩展。** 会话状态保存在内存中且为单进程。目前多实例部署需要粘性路由或外部会话协调。
- [x] **会话重连（服务端）。** 断开的连接现在可以通过 ICE restart（`PATCH /whip/{sessionId}`）在同一会话上恢复，而不再意味着通话结束。`PeerConnection`、DTLS 关联、正在运行的流水线与 LLM 客户端全部保留，因此对话历史与滚动摘要也得以保留，开场白不会重播。客户端凭空消失后残留的会话，会在 `server.session_grace_ms` 之后被回收。见[协议参考 → ICE 重启](./protocol.zh-CN.md#ice-重启)。
- [x] **客户端侧重连。** TypeScript、React Native、Go 与 Rust SDK 会自行发现断开、重新收集 ICE 并发送该 PATCH，在连接被判定为 failed 之前的约 25 秒窗口内以退避方式重试三次。除了按需响应新的 `reconnecting` 状态之外，宿主应用无需做任何事。交互过程见[协议参考 → ICE 重启](./protocol.zh-CN.md#ice-重启)，可调参数见各 SDK 的 README。
- [ ] **Python SDK 的重连。** Python 是唯一无法驱动上述流程的客户端：aiortc 没有 ICE restart 原语（`createOffer()` 不接受参数，aioice 的凭据在构造时即固定），也没有可供触发的 `disconnected` 连接状态 —— 它会直接从 `connected` 变为 `failed`。协议层的辅助函数已随 `streamcore.icerestart` 提供，供使用其他 WebRTC 栈的调用方使用；但网络发生变化的 Python 客户端仍会重新拨号、开启一个新会话。
- [ ] **指标与可观测性。** 已有 `/health` 与 DataChannel 时延事件，但没有 Prometheus/OpenTelemetry 导出。

## 接入自有智能体

- [ ] **HTTP 智能体端点。** 一个可配置的、兼容 OpenAI 或 webhook 形式的智能体后端，让[接入自有智能体](./bring-your-own-agent.zh-CN.md)完全不需要写 Go 代码。目前的等价做法是实现一个很小的 Go 接口。
- [ ] **跨会话的持久记忆。** 滚动摘要随通话开始、随通话结束。

## 生态

- [ ] **更多示例**，以印证产品定位：实时翻译、AI 主持的语音房间、浏览器副驾、嵌入式设备、SIP 应用，以及一个完全不含 LLM 的纯音频处理应用。
- [ ] **嵌入式客户端加固。** [`esp32`](https://github.com/streamcoreai/esp32) 中的 ESP32-S3 固件已能通过 WHIP 连接，但尚未达到生产可用。
- [ ] **`streamcore-cli` 发布。** RAG 的文档入库文档所依赖的二进制程序尚未公开 —— 见[智能体运行时 → 文档入库](./agent-runtime.zh-CN.md#文档入库)。
- [ ] **React Native SDK 发布到 npm。** `@streamcore/react-native-sdk` 已完成、可从源码使用，但尚未发布。

---

已经交付并受支持的部分：[能力清单](./capabilities.zh-CN.md)。
