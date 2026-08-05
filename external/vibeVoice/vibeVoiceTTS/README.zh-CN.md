# VibeVoice TTS Server

[English](./README.md) | **简体中文**

使用 Microsoft VibeVoice-Realtime-0.5B 的 HTTP 文字转语音服务。接收 JSON 请求，返回原始 PCM 音频。

## 模型

| 平台 | 模型 | 后端 |
|----------|-------|---------|
| Apple Silicon | `mlx-community/VibeVoice-Realtime-0.5B-6bit` | mlx-audio |
| Linux / CUDA | `microsoft/VibeVoice-Realtime-0.5B` | PyTorch |

## 安装

```bash
pip install -r requirements.txt

# Then install one backend:
pip install mlx-audio          # Apple Silicon
# OR
pip install torch transformers  # PyTorch (basic)
# OR (recommended for PyTorch):
# git clone https://github.com/microsoft/VibeVoice && cd VibeVoice
# pip install -e .[streamingtts]
```

## 运行

```bash
python server.py
# http://127.0.0.1:8300

python server.py --port 9100 --model mlx-community/VibeVoice-Realtime-0.5B-fp16
```

## API

### POST /synthesize

```bash
curl -X POST http://localhost:8300/synthesize \
  -H "Content-Type: application/json" \
  -d '{"text": "Hello world", "voice": "en-Emma_woman"}' \
  --output speech.pcm
```

**请求体：**
```json
{"text": "Hello world", "voice": "en-Emma_woman"}
```

**响应：** `audio/pcm` —— 原始 PCM 字节（16 kHz、16 位有符号小端、单声道）

### GET /health

返回 `{"status": "ok"}`。

## 选项

| 参数 | 默认值 | 说明 |
|------|---------|-------------|
| `--host` | `127.0.0.1` | 绑定地址 |
| `--port` | `8300` | 绑定端口 |
| `--model` | 自动（MLX 6-bit 或 PyTorch） | HuggingFace 模型名 |
| `--log-level` | `INFO` | 日志级别 |
