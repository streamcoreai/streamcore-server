# VibeVoice ASR Server

[English](./README.md) | **简体中文**

使用 Microsoft VibeVoice-ASR 的实时流式语音转文字服务。通过 WebSocket 接收原始 PCM 音频，返回 JSON 转写事件。

## 模型

| 平台 | 模型 | 后端 |
|----------|-------|---------|
| Apple Silicon | `mlx-community/VibeVoice-ASR-4bit` | mlx-audio |
| Linux / CUDA | `microsoft/VibeVoice-ASR` | PyTorch + transformers |

## 安装

```bash
pip install -r requirements.txt

# Then install one backend:
pip install mlx-audio          # Apple Silicon
# OR
pip install torch transformers librosa  # PyTorch
```

## 运行

```bash
python server.py
# ws://127.0.0.1:8200

python server.py --port 9000 --model mlx-community/VibeVoice-ASR-bf16
python server.py --silence-timeout 1.0 --energy-threshold 400
```

## 协议

- **客户端 → 服务端**：二进制 WebSocket 帧 —— 原始 PCM（16 kHz、16 位有符号小端、单声道）
- **服务端 → 客户端**：JSON 文本帧

```json
{"text": "hello how are you", "is_final": false}
{"text": "hello how are you doing", "is_final": true}
```

服务端会缓冲进入的音频，用基于能量的 VAD 检测语音边界，并在检测到静音时（默认约 800 ms）进行转写。长句期间每约 3 秒发出一次中间结果。

## 选项

| 参数 | 默认值 | 说明 |
|------|---------|-------------|
| `--host` | `127.0.0.1` | 绑定地址 |
| `--port` | `8200` | 绑定端口 |
| `--model` | 自动（MLX 4-bit 或 PyTorch） | HuggingFace 模型名 |
| `--silence-timeout` | `0.8` | 出最终结果前的静音秒数 |
| `--energy-threshold` | `500` | 语音检测的 RMS 能量阈值 |
| `--log-level` | `INFO` | 日志级别 |
