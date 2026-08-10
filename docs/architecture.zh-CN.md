[English](./architecture.md) | **简体中文**

# 架构与实现

```text
┌─────────────────────┐                    ┌─────────────────────────────────────┐
│  Client / SDK / SIP │                    │      StreamCore Runtime (Go)        │
│                     │                    │                                     │
│  Mic → WebRTC ──────┼──── Opus RTP ──────┼──→ Opus decode → VAD → STT          │
│  Speaker ← WebRTC ←─┼──── Opus RTP ←─────┼──← Opus encode ← TTS                │
│                     │                    │               │                     │
│  HTTP POST ─────────┼── WHIP (SDP) ──────┼──→ Peer + session created           │
│  DataChannel ◄──────┼──── events   ←─────┼──← transcript · response · state    │
│                     │                    │               │                     │
│                     │                    │               ├── your LLM client   │
│                     │                    │               ├── RAG context       │
│                     │                    │               ├── Skills prompt     │
│                     │                    │               ├── Plugin runtime    │
│                     │                    │               │   ├── Python        │
│                     │                    │               │   ├── TypeScript    │
│                     │                    │               │   └── JavaScript    │
│                     │                    │               └── Native Go tools   │
└─────────────────────┘                    └─────────────────────────────────────┘
```

**媒体流转。** 麦克风音频通过 WebRTC 到达，按 20 毫秒一帧解码为 PCM，经过 VAD，再流式送往 STT。final 转写交给模型层；模型的流式输出按句边界切分，边到达边交给 TTS，因此合成在生成结束之前就已开始。合成出的 PCM 重新编码为 Opus 并写回 RTP 流。转写、回复与状态文本则并行地走 DataChannel。

**为什么用 Go。** 时延敏感的链路用 Go 加 [Pion](https://github.com/pion/webrtc) 实现：每个阶段一个 goroutine，阶段之间用有界 channel 连接，热路径上没有 GC 压力大的缓冲。RTP 读取、Opus 解码、VAD、STT 流式、编排、TTS、Opus 编码与 RTP 写出各自独立成一个阶段。这是为了可预测的每轮时延而做的实现选择 —— 你真正面对的接口是 SDK 与事件协议，语言可以任选。

## 包结构

| 包 | 职责 |
|---------|----------------|
| [`internal/signaling`](../internal/signaling/) | WHIP 处理器、SDP 交换、会话 URL |
| [`internal/peer`](../internal/peer/) | Pion peer connection、轨道、DataChannel |
| [`internal/session`](../internal/session/) | 会话管理器、多 peer 生命周期 |
| [`internal/pipeline`](../internal/pipeline/) | 入向/出向音频、智能体循环、barge-in、思考音 |
| [`internal/audio`](../internal/audio/) | Opus 编解码、RTP 组包 |
| [`internal/vad`](../internal/vad/) | 基于能量的语音活动检测 |
| [`internal/stt`](../internal/stt/)、[`internal/tts`](../internal/tts/)、[`internal/llm`](../internal/llm/) | 服务商适配层 |
| [`internal/plugin`](../internal/plugin/) | 插件运行时、原生工具、技能 |
| [`internal/rag`](../internal/rag/) | 检索与 embedding |
| [`internal/turn`](../internal/turn/) | 内置 STUN/TURN 服务 |

线上协议细节见[协议参考](./protocol.zh-CN.md)。
