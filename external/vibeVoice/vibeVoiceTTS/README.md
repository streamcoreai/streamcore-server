# VibeVoice TTS Server

HTTP text-to-speech server using Microsoft VibeVoice-Realtime-0.5B. Accepts JSON requests and returns raw PCM audio.

## Models

| Platform | Model | Backend |
|----------|-------|---------|
| Apple Silicon | `mlx-community/VibeVoice-Realtime-0.5B-6bit` | mlx-audio |
| Linux / CUDA | `microsoft/VibeVoice-Realtime-0.5B` | PyTorch |

## Install

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

## Run

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

**Request body:**
```json
{"text": "Hello world", "voice": "en-Emma_woman"}
```

**Response:** `audio/pcm` — raw PCM bytes (16 kHz, 16-bit signed LE, mono)

### GET /health

Returns `{"status": "ok"}`.

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `--host` | `127.0.0.1` | Bind host |
| `--port` | `8300` | Bind port |
| `--model` | auto (MLX 6-bit or PyTorch) | HuggingFace model name |
| `--log-level` | `INFO` | Logging level |
