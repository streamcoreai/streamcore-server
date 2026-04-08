# VibeVoice ASR Server

Live streaming speech-to-text server using Microsoft VibeVoice-ASR. Accepts raw PCM audio over WebSocket and returns JSON transcript events.

## Models

| Platform | Model | Backend |
|----------|-------|---------|
| Apple Silicon | `mlx-community/VibeVoice-ASR-4bit` | mlx-audio |
| Linux / CUDA | `microsoft/VibeVoice-ASR` | PyTorch + transformers |

## Install

```bash
pip install -r requirements.txt

# Then install one backend:
pip install mlx-audio          # Apple Silicon
# OR
pip install torch transformers librosa  # PyTorch
```

## Run

```bash
python server.py
# ws://127.0.0.1:8200

python server.py --port 9000 --model mlx-community/VibeVoice-ASR-bf16
python server.py --silence-timeout 1.0 --energy-threshold 400
```

## Protocol

- **Client → Server**: binary WebSocket frames — raw PCM (16 kHz, 16-bit signed LE, mono)
- **Server → Client**: JSON text frames

```json
{"text": "hello how are you", "is_final": false}
{"text": "hello how are you doing", "is_final": true}
```

The server buffers incoming audio, detects speech boundaries via energy-based VAD, and transcribes when silence is detected (~800 ms default). Partial results are emitted every ~3 seconds during long utterances.

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `--host` | `127.0.0.1` | Bind host |
| `--port` | `8200` | Bind port |
| `--model` | auto (MLX 4-bit or PyTorch) | HuggingFace model name |
| `--silence-timeout` | `0.8` | Seconds of silence before final result |
| `--energy-threshold` | `500` | RMS energy threshold for speech detection |
| `--log-level` | `INFO` | Logging level |
