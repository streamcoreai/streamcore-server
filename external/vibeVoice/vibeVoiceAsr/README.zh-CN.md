# VibeVoice ASR Server

[English](./README.md) | **简体中文**

使用 Microsoft VibeVoice-ASR 的实时流式语音转文字服务。通过 WebSocket 接收原始 PCM 音频，返回 JSON 转写事件。

## 模型

| 平台 | 模型 | 后端 |
|----------|-------|---------|
| Apple Silicon | `mlx-community/VibeVoice-ASR-4bit` | mlx-audio |
| Linux / CUDA | `microsoft/VibeVoice-ASR-HF` | PyTorch + transformers ≥ 5.3 |

PyTorch 后端通过 transformers 原生的 `VibeVoiceAsrForConditionalGeneration` 加载模型，因此需要 `-HF` 版本的权重。原始的 `microsoft/VibeVoice-ASR` 只能通过微软的 `vibevoice` 包加载，在这里无法使用。

## 安装

```bash
pip install -r requirements.txt

# Then install one backend:
pip install mlx-audio          # Apple Silicon
# OR
pip install torch "transformers>=5.3.0" accelerate librosa  # PyTorch
```

## 运行

```bash
python server.py
# ws://127.0.0.1:8200

python server.py --port 9000 --model mlx-community/VibeVoice-ASR-bf16
python server.py --silence-timeout 1.0 --vad-threshold 0.6
```

## 协议

- **客户端 → 服务端**：二进制 WebSocket 帧 —— 原始 PCM（16 kHz、16 位有符号小端、单声道）
- **服务端 → 客户端**：JSON 文本帧

```json
{"text": "hello how are you", "is_final": false}
{"text": "hello how are you doing", "is_final": true}
```

服务端会缓冲进入的音频，用 Silero VAD 检测语音边界，并在检测到静音时（默认约 800 ms）进行转写。只发出最终结果。

## 选项

| 参数 | 默认值 | 说明 |
|------|---------|-------------|
| `--host` | `127.0.0.1` | 绑定地址 |
| `--port` | `8200` | 绑定端口 |
| `--model` | 自动（MLX 4-bit 或 PyTorch） | HuggingFace 模型名 |
| `--silence-timeout` | `0.8` | 出最终结果前的静音秒数 |
| `--vad-threshold` | `0.5` | Silero VAD 语音概率阈值（0.0-1.0） |
| `--log-level` | `INFO` | 日志级别 |
