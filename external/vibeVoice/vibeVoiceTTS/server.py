#!/usr/bin/env python3
"""
VibeVoice TTS - HTTP text-to-speech server.

POST /synthesize  →  raw PCM audio (16kHz, 16-bit signed LE, mono)
GET  /health      →  {"status":"ok"}

Uses mlx-audio on Apple Silicon, falls back to PyTorch on other platforms.
"""

import argparse
import logging
import platform

import numpy as np
from fastapi import FastAPI, HTTPException
from fastapi.responses import Response
from pydantic import BaseModel
import uvicorn

logger = logging.getLogger("vibevoice-tts")

TARGET_SAMPLE_RATE = 16000


def is_apple_silicon():
    return platform.system() == "Darwin" and platform.machine() == "arm64"


_model = None
_backend = None
_model_sample_rate = 24000  # VibeVoice-Realtime outputs 24 kHz


def load_model(model_name: str):
    """Load TTS model. Auto-selects MLX on Apple Silicon, PyTorch otherwise."""
    global _model, _backend, _model_sample_rate

    if is_apple_silicon():
        try:
            from mlx_audio.tts.utils import load_model as mlx_load

            logger.info("Using MLX backend")
            logger.info(f"Loading model: {model_name}")
            _model = mlx_load(model_name)
            _backend = "mlx"
            logger.info("TTS model loaded successfully")
            return
        except ImportError:
            logger.warning("mlx-audio not installed, falling back to PyTorch")

    # PyTorch fallback
    try:
        logger.info("Using PyTorch backend")
        logger.info(f"Loading model: {model_name}")

        # VibeVoice-Realtime-0.5B uses the vibevoice package from
        # https://github.com/microsoft/VibeVoice
        # Install: pip install -e .[streamingtts]  (from cloned repo)
        from vibevoice.realtime.model import VibeVoiceRealtimeModel

        _model = VibeVoiceRealtimeModel.from_pretrained(model_name)
        _backend = "pytorch"
        logger.info("TTS model loaded successfully")
    except ImportError:
        # Lighter fallback: try loading through transformers directly
        try:
            import torch
            from transformers import AutoModelForCausalLM, AutoProcessor

            processor = AutoProcessor.from_pretrained(
                model_name, trust_remote_code=True
            )
            model = AutoModelForCausalLM.from_pretrained(
                model_name,
                trust_remote_code=True,
                torch_dtype=(
                    torch.float16 if torch.cuda.is_available() else torch.float32
                ),
                device_map="auto" if torch.cuda.is_available() else None,
            )
            _model = {"processor": processor, "model": model}
            _backend = "pytorch_transformers"
            logger.info("TTS model loaded via transformers")
        except Exception as e:
            raise RuntimeError(
                f"Failed to load TTS model: {e}\n"
                "Install mlx-audio (Apple Silicon): pip install mlx-audio\n"
                "Install PyTorch: pip install vibevoice  OR  pip install torch transformers"
            )


def resample(audio: np.ndarray, orig_sr: int, target_sr: int) -> np.ndarray:
    """Resample audio using linear interpolation (fast, good enough for speech)."""
    if orig_sr == target_sr:
        return audio
    ratio = target_sr / orig_sr
    n_out = int(len(audio) * ratio)
    indices = np.arange(n_out) / ratio
    indices_floor = np.floor(indices).astype(int)
    indices_ceil = np.minimum(indices_floor + 1, len(audio) - 1)
    frac = indices - indices_floor
    return audio[indices_floor] * (1 - frac) + audio[indices_ceil] * frac


def synthesize_speech(text: str, voice: str = "en-Emma_woman") -> bytes:
    """Generate speech from text and return raw PCM bytes (16 kHz, s16le, mono)."""
    if _backend == "mlx":
        audio_chunks = []
        for result in _model.generate(text, voice=voice):
            chunk = np.array(result.audio, dtype=np.float32)
            audio_chunks.append(chunk)

        if not audio_chunks:
            return b""

        audio = np.concatenate(audio_chunks)

        # Resample to 16 kHz
        audio = resample(audio, _model_sample_rate, TARGET_SAMPLE_RATE)

        # Float → int16 PCM
        audio = np.clip(audio * 32767, -32768, 32767).astype(np.int16)
        return audio.tobytes()

    elif _backend == "pytorch":
        # vibevoice package path
        audio = _model.synthesize(
            text, speaker_name=voice.split("-")[-1] if "-" in voice else voice
        )
        if isinstance(audio, np.ndarray):
            pass
        else:
            import torch

            audio = audio.cpu().numpy()

        audio = audio.astype(np.float32)
        audio = resample(audio, _model_sample_rate, TARGET_SAMPLE_RATE)
        audio = np.clip(audio * 32767, -32768, 32767).astype(np.int16)
        return audio.tobytes()

    elif _backend == "pytorch_transformers":
        import torch

        processor = _model["processor"]
        model = _model["model"]

        inputs = processor(text=text, return_tensors="pt", trust_remote_code=True)
        if torch.cuda.is_available():
            inputs = {k: v.cuda() for k, v in inputs.items()}

        with torch.no_grad():
            output = model.generate(**inputs, max_new_tokens=4096)

        # Extract audio from model output (implementation depends on model)
        audio = output.cpu().numpy().astype(np.float32)
        audio = resample(audio, _model_sample_rate, TARGET_SAMPLE_RATE)
        audio = np.clip(audio * 32767, -32768, 32767).astype(np.int16)
        return audio.tobytes()

    return b""


# ── FastAPI app ──────────────────────────────────────────────────────────────

app = FastAPI(title="VibeVoice TTS")


class SynthesizeRequest(BaseModel):
    text: str
    voice: str = "en-Emma_woman"


@app.post("/synthesize")
async def synthesize_endpoint(req: SynthesizeRequest):
    if not req.text.strip():
        raise HTTPException(status_code=400, detail="text must not be empty")
    try:
        import asyncio

        pcm_bytes = await asyncio.get_event_loop().run_in_executor(
            None, synthesize_speech, req.text, req.voice
        )
        logger.info(f"Synthesized {len(pcm_bytes)} bytes for {len(req.text)} chars")
        return Response(content=pcm_bytes, media_type="audio/pcm")
    except Exception as e:
        logger.error(f"Synthesis error: {e}", exc_info=True)
        raise HTTPException(status_code=500, detail=str(e))


@app.get("/health")
async def health():
    return {"status": "ok"}


# ── Entrypoint ───────────────────────────────────────────────────────────────

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="VibeVoice TTS HTTP Server")
    parser.add_argument("--host", default="127.0.0.1", help="Bind host")
    parser.add_argument("--port", type=int, default=8300, help="Bind port")
    parser.add_argument("--model", default=None, help="Model name or path")
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()

    logging.basicConfig(
        level=getattr(logging, args.log_level.upper()),
        format="%(asctime)s [%(name)s] %(levelname)s: %(message)s",
    )

    if args.model is None:
        args.model = (
            "mlx-community/VibeVoice-Realtime-0.5B-6bit"
            if is_apple_silicon()
            else "microsoft/VibeVoice-Realtime-0.5B"
        )

    load_model(args.model)

    uvicorn.run(app, host=args.host, port=args.port, log_level=args.log_level.lower())
