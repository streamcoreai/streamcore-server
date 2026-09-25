# Moonshine STT Server

[English](./README.md) | **简体中文**

使用 [Moonshine](https://moonshine.ai) 的实时流式语音转文字服务。通过 WebSocket 接收原始 PCM 音频，返回 Deepgram 的实时转写帧，因此 `stt.provider = "moonshine"` 走的是服务端已经为 Deepgram 准备好的那条合并与去重路径。

全部在本机运行。无需 API key，无需账号 —— 模型首次运行时下载并缓存。

## 模型

目录会按语言选择默认模型，`--model-arch` 可覆盖。

| 架构 | 说明 |
|------|-------|
| `tiny`、`base` | 非流式，占用最小 |
| `tiny-streaming`、`base-streaming` | 为实时音频设计 |
| `small-streaming`、`medium-streaming` | 准确率更高，算力开销更大 |

英语模型为 MIT 许可。其他语言使用非商业的 [Moonshine Community License](https://www.moonshine.ai/license)，加载时服务端会打印提示。

## 安装

```bash
pip install -r requirements.txt
```

## 运行

```bash
python server.py
# ws://127.0.0.1:8210

python server.py --port 9000 --model-arch base-streaming
python server.py --language es --update-interval 0.8
```

## 协议

- **客户端 → 服务端**：二进制 WebSocket 帧 —— 原始 PCM（16 kHz、16 位有符号小端、单声道）
- **客户端 → 服务端**：JSON 文本帧 —— `{"type": "KeepAlive" | "Finalize" | "CloseStream"}`
- **服务端 → 客户端**：Deepgram JSON 帧 —— `Results`、`SpeechStarted`、`UtteranceEnd`、`Metadata`

```json
{"type":"SpeechStarted","channel":[0,1],"timestamp":0.42}
{"type":"Results","channel_index":[0,1],"start":0.42,"duration":0.9,"is_final":false,"speech_final":false,"channel":{"alternatives":[{"transcript":"turn on"}]}}
{"type":"Results","channel_index":[0,1],"start":0.42,"duration":1.8,"is_final":true,"speech_final":true,"channel":{"alternatives":[{"transcript":"turn on the lights"}]}}
{"type":"UtteranceEnd","channel":[0,1],"last_word_end":2.22}
```

Moonshine 只在一句话结束时才完成一行转写，所以它发出的每个 `is_final` 同时也是 `speech_final`。Deepgram 之所以区分两者，是因为它会在句子中途就冻结部分词。

没有 confidence 字段：Moonshine 不给出该值，服务端也不会编造模型没产出的数字。Go 客户端把缺失的字段解析为 0，管线将其理解为「未知」而非「低置信度」。

## 选项

| 参数 | 默认值 | 说明 |
|------|---------|-------------|
| `--host` | `127.0.0.1` | 绑定地址 |
| `--port` | `8210` | 绑定端口 |
| `--language` | `en` | 两字母语言代码 |
| `--model-arch` | 目录默认值 | `tiny`、`base`、`tiny-streaming`、`base-streaming`、`small-streaming`、`medium-streaming` |
| `--update-interval` | `0.5` | 两次转写之间的音频秒数 |
| `--log-level` | `INFO` | 日志级别 |

`--update-interval` 是下限而不是固定节奏。每一轮至少要覆盖与上一轮耗时相当的音频量，因此跟不上的机器会让每轮覆盖更多音频，而不是每一轮都落后得更远。
