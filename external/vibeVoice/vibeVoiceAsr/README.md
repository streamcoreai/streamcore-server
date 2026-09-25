# VibeVoice ASR Server

**English** | [简体中文](./README.zh-CN.md)

Live streaming speech-to-text server using Microsoft VibeVoice-ASR. Accepts raw PCM audio over WebSocket and returns JSON transcript events.

## Models

| Platform | Model | Backend |
|----------|-------|---------|
| Apple Silicon | `mlx-community/VibeVoice-ASR-4bit` | mlx-audio |
| Linux / CUDA | `microsoft/VibeVoice-ASR-HF` | PyTorch + transformers ≥ 5.3 |

The PyTorch backend loads the model with transformers' native `VibeVoiceAsrForConditionalGeneration`, so it needs the `-HF` checkpoint. The original `microsoft/VibeVoice-ASR` checkpoint only loads through Microsoft's `vibevoice` package and won't work here.

## Install

```bash
pip install -r requirements.txt

# Then install one backend:
pip install mlx-audio          # Apple Silicon
# OR
pip install torch "transformers>=5.3.0" accelerate librosa  # PyTorch
```

## Run

```bash
python server.py
# ws://127.0.0.1:8200

python server.py --port 9000 --model mlx-community/VibeVoice-ASR-bf16
python server.py --silence-timeout 1.0 --vad-threshold 0.6
```

## Protocol

- **Client → Server**: binary WebSocket frames — raw PCM (16 kHz, 16-bit signed LE, mono)
- **Server → Client**: JSON text frames

```json
{"text": "hello how are you", "is_final": false}
{"text": "hello how are you doing", "is_final": true}
```

The server buffers incoming audio, detects speech boundaries with Silero VAD, and transcribes when silence is detected (~800 ms default). Only final results are emitted.

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `--host` | `127.0.0.1` | Bind host |
| `--port` | `8200` | Bind port |
| `--model` | auto (MLX 4-bit or PyTorch) | HuggingFace model name |
| `--silence-timeout` | `0.8` | Seconds of silence before final result |
| `--vad-threshold` | `0.5` | Silero VAD speech probability threshold (0.0-1.0) |
| `--log-level` | `INFO` | Logging level |
