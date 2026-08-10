[English](./roadmap.md) | **简体中文**

# 路线图 / TODO

本页列出的全部内容都是**尚未实现**的。把它们放在这里，是为了让[能力清单](./capabilities.zh-CN.md)保持诚实 —— 只要出现在本页，就不要按它来做今天的规划。

需要其中某一项？在 [Discord](https://discord.gg/xKGFaGWawT) 说一声或提一个 issue —— 需求会改变这份清单的顺序。欢迎贡献代码，见 [CONTRIBUTING.md](../CONTRIBUTING.md)。

## 生产环境加固

- [ ] **水平扩展。** 会话状态保存在内存中且为单进程。目前多实例部署需要粘性路由或外部会话协调。
- [ ] **会话重连。** 没有 ICE restart，也没有恢复路径；连接断开就意味着一个新会话。
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
